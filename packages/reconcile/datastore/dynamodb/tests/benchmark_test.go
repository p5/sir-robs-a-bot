package dynamodbtests

import (
	"bytes"
	"crypto/rand"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	adapter "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/dynamodb"
)

type countingHTTP struct {
	requests atomic.Int64
	queries  atomic.Int64
}

func (client *countingHTTP) Do(request *http.Request) (*http.Response, error) {
	client.requests.Add(1)
	if request.Header.Get("X-Amz-Target") == "DynamoDB_20120810.Query" {
		client.queries.Add(1)
	}
	return http.DefaultClient.Do(request)
}

func BenchmarkDispatchBacklog(b *testing.B) {
	for _, records := range []int{0, 256} {
		b.Run(strconv.Itoa(records), func(b *testing.B) {
			config := adapter.Config{Table: table, Namespace: "bench-" + rand.Text()}
			store, err := adapter.New(newClient(), config)
			if err != nil {
				b.Fatal(err)
			}
			for index := range records {
				key := datastore.Key{Kind: "bench", ID: "a-" + strconv.Itoa(index)}
				if err := store.Enqueue(b.Context(), key); err != nil {
					b.Fatal(err)
				}
				claim, err := store.Claim(b.Context(), []string{key.Kind}, time.Minute)
				if err != nil {
					b.Fatal(err)
				}
				if err := store.Commit(b.Context(), claim, datastore.Completion{}); err != nil {
					b.Fatal(err)
				}
			}
			transport := new(countingHTTP)
			options := newClient().Options()
			options.HTTPClient = transport
			store, err = adapter.New(sdk.New(options), config)
			if err != nil {
				b.Fatal(err)
			}
			key := datastore.Key{Kind: "bench", ID: "z-hot"}
			// Create before timing so request counts represent an existing key.
			if err := store.Enqueue(b.Context(), key); err != nil {
				b.Fatal(err)
			}
			transport.requests.Store(0)
			transport.queries.Store(0)
			b.ReportAllocs()
			for b.Loop() {
				if err := store.Enqueue(b.Context(), key); err != nil {
					b.Fatal(err)
				}
				claim, err := store.Claim(b.Context(), []string{key.Kind}, time.Minute)
				if err != nil {
					b.Fatal(err)
				}
				if err := store.Commit(b.Context(), claim, datastore.Completion{}); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(transport.requests.Load())/float64(b.N), "requests/op")
			b.ReportMetric(float64(transport.queries.Load())/float64(b.N), "queries/op")
		})
	}
}

// BenchmarkExpiredTimerBurst measures a synchronized expiry, including competing
// controllers. Preparation is untimed. Each iteration uses a fresh namespace.
// Request counts include discovery, promotion, ownership, and contention.
func BenchmarkExpiredTimerBurst(b *testing.B) {
	for _, timers := range []int{0, 32, 128, 512} {
		for _, controllers := range []int{1, 4} {
			b.Run(fmt.Sprintf("timers=%d/controllers=%d", timers, controllers), func(b *testing.B) {
				var requests, queries, transactions int64
				var firstClaim time.Duration
				for b.Loop() {
					b.StopTimer()
					stores, transport := prepareTimerBurst(b, timers, controllers)
					type outcome struct {
						claim   datastore.Claim
						err     error
						elapsed time.Duration
					}
					results := make(chan outcome, controllers)
					b.StartTimer()
					started := time.Now()
					for _, store := range stores {
						go func() {
							claim, err := store.Claim(b.Context(), []string{"burst"}, time.Minute)
							results <- outcome{claim: claim, err: err, elapsed: time.Since(started)}
						}()
					}
					earliest := time.Duration(math.MaxInt64)
					seen := make(map[datastore.Key]bool)
					for range controllers {
						result := <-results
						if errors.Is(result.err, datastore.ErrNoWork) {
							continue
						}
						if result.err != nil {
							b.Fatal(result.err)
						}
						if seen[result.claim.Key] {
							b.Fatal("two controllers received the same resource")
						}
						seen[result.claim.Key] = true
						earliest = min(earliest, result.elapsed)
						if controllers == 1 && result.claim.Priority != uint32(timers+1) {
							b.Fatalf("highest expired priority not selected: %+v", result.claim)
						}
					}
					b.StopTimer()
					if len(seen) != min(controllers, timers+1) {
						b.Fatalf("claims=%d, expected=%d", len(seen), min(controllers, timers+1))
					}
					firstClaim += earliest
					requests += transport.requests.Load()
					queries += transport.queries.Load()
					transactions += transport.transactions.Load()
					b.StartTimer()
				}
				b.ReportMetric(float64(requests)/float64(b.N), "requests/burst")
				b.ReportMetric(float64(queries)/float64(b.N), "queries/burst")
				b.ReportMetric(float64(transactions)/float64(b.N), "transactions/burst")
				b.ReportMetric(float64(firstClaim.Nanoseconds())/float64(b.N), "first-claim-ns")
			})
		}
	}
}

type burstCountingHTTP struct {
	countingHTTP
	transactions  atomic.Int64
	scheduleItems atomic.Int64
}

func (client *burstCountingHTTP) Do(request *http.Request) (*http.Response, error) {
	if request.Header.Get("X-Amz-Target") == "DynamoDB_20120810.TransactWriteItems" {
		client.transactions.Add(1)
	}
	response, err := client.countingHTTP.Do(request)
	if err != nil || request.Header.Get("X-Amz-Target") != "DynamoDB_20120810.Query" {
		return response, err
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		return nil, err
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	var output struct{ Count int64 }
	if err := json.Unmarshal(body, &output); err != nil {
		response.Body.Close()
		return nil, err
	}
	client.scheduleItems.Add(output.Count)
	return response, nil
}

func prepareTimerBurst(b *testing.B, timers, controllers int) ([]*adapter.Store, *burstCountingHTTP) {
	b.Helper()
	clock := newClock()
	config := adapter.Config{Table: table, Namespace: "burst-" + rand.Text(), Clock: clock.now}
	setup, err := adapter.New(newClient(), config)
	if err != nil {
		b.Fatal(err)
	}
	// Previously delayed records become eligible together. The
	// last inserted timer has the highest numeric priority.
	for index := range timers {
		key := datastore.Key{Kind: "burst", ID: strconv.Itoa(index)}
		if err := setup.Enqueue(b.Context(), key, datastore.EnqueueOptions{Priority: uint32(index + 2)}); err != nil {
			b.Fatal(err)
		}
		claim, err := setup.Claim(b.Context(), []string{key.Kind}, time.Minute)
		if err != nil {
			b.Fatal(err)
		}
		if err := setup.Commit(b.Context(), claim, datastore.Completion{Again: true, After: time.Hour}); err != nil {
			b.Fatal(err)
		}
	}
	knownReady := datastore.Key{Kind: "burst", ID: "known-ready"}
	if err := setup.Enqueue(b.Context(), knownReady, datastore.EnqueueOptions{Priority: 1}); err != nil {
		b.Fatal(err)
	}
	transport := new(burstCountingHTTP)
	stores := make([]*adapter.Store, controllers)
	for index := range stores {
		options := newClient().Options()
		options.HTTPClient = transport
		stores[index], err = adapter.New(sdk.New(options), config)
		if err != nil {
			b.Fatal(err)
		}
		// Cache immutable settings outside the measurement.
		if _, err := stores[index].Get(b.Context(), knownReady); err != nil {
			b.Fatal(err)
		}
	}
	clock.advance(time.Hour)
	transport.requests.Store(0)
	transport.queries.Store(0)
	transport.transactions.Store(0)
	transport.scheduleItems.Store(0)
	return stores, transport
}

// BenchmarkExpiredTimerDrain includes all claims and completions after expiry.
// It catches designs that improve first dispatch by repeatedly scanning the burst.
func BenchmarkExpiredTimerDrain(b *testing.B) {
	for _, timers := range []int{128, 512} {
		b.Run(strconv.Itoa(timers), func(b *testing.B) {
			var requests, queries, transactions, scheduleItems int64
			for b.Loop() {
				b.StopTimer()
				stores, transport := prepareTimerBurst(b, timers, 1)
				store := stores[0]
				b.StartTimer()
				claims := 0
				for {
					claim, err := store.Claim(b.Context(), []string{"burst"}, time.Minute)
					if errors.Is(err, datastore.ErrNoWork) {
						break
					}
					if err != nil {
						b.Fatal(err)
					}
					claims++
					if err := store.Commit(b.Context(), claim, datastore.Completion{}); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				if claims != timers+1 {
					b.Fatalf("drained %d, expected %d", claims, timers+1)
				}
				requests += transport.requests.Load()
				queries += transport.queries.Load()
				transactions += transport.transactions.Load()
				scheduleItems += transport.scheduleItems.Load()
				b.StartTimer()
			}
			b.ReportMetric(float64(requests)/float64(b.N), "requests/drain")
			b.ReportMetric(float64(queries)/float64(b.N), "queries/drain")
			b.ReportMetric(float64(transactions)/float64(b.N), "transactions/drain")
			b.ReportMetric(float64(scheduleItems)/float64(b.N), "schedule-items/drain")
		})
	}
}
