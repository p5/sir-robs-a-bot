package gitlab

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
)

func TestClientRejectsUnsafeOrigins(t *testing.T) {
	for _, origin := range []string{"http://gitlab.com", "https://user:secret@gitlab.com", "https://gitlab.com/path", "https://gitlab.com?token=x", "https://gitlab.com#fragment", "//gitlab.com"} {
		if _, err := NewClient(http.DefaultClient, origin, "token"); err == nil {
			t.Errorf("accepted unsafe origin %q", origin)
		}
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	reached := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer origin.Close()
	client, err := NewClient(origin.Client(), origin.URL, "private-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Identity(t.Context()); err == nil || reached {
		t.Fatalf("redirect followed: reached=%v err=%v", reached, err)
	}
}

func TestReactionLostReplyRecoversByLookup(t *testing.T) {
	posted, exists := 0, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "token" {
			t.Error("missing explicit credential")
		}
		if r.Method == "GET" {
			if exists {
				fmt.Fprint(w, `[{"id":9,"name":"eyes","user":{"id":42}}]`)
			} else {
				fmt.Fprint(w, `[]`)
			}
			return
		}
		posted++
		exists = true
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		connection.Close()
	}))
	defer server.Close()
	client, err := NewClient(server.Client(), server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := client.reactEyes(t.Context(), "/projects/11/issues/3/notes/1/award_emoji", 42); err != nil {
			t.Fatal(err)
		}
	}
	if posted != 1 {
		t.Fatalf("repeated create after lost reply: %d", posted)
	}
}

func TestUnknownReactionStopsAutomaticRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			fmt.Fprint(w, `[]`)
		} else {
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	client, _ := NewClient(server.Client(), server.URL, "token")
	sender := Acknowledger{Client: client, AccountID: 42}
	request := intake.Request{Source: intake.Source{Connection: client.Connection(42), Scope: "11", Kind: "note", ID: "1"}, Metadata: jsontext.Value(`{"ProjectID":11,"Resource":"issues","IID":3}`)}
	err := sender.Acknowledge(t.Context(), request)
	failure, ok := errors.AsType[*intake.DeliveryError](err)
	if !ok || failure.Disposition != intake.DeliveryUncertain {
		t.Fatalf("uncertain POST was retryable: %v", err)
	}
}

func TestPollerBoundsPaginationAndHonorsRateLimits(t *testing.T) {
	for _, status := range []int{200, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Retry-After", "3600")
				w.Header().Set("X-Next-Page", "https://foreign.invalid")
				w.WriteHeader(status)
				fmt.Fprint(w, `[]`)
			}))
			defer server.Close()
			client, _ := NewClient(server.Client(), server.URL, "token")
			err := client.pages(t.Context(), "/todos", func([]byte) error { return nil })
			if err == nil {
				t.Fatal("unbounded pagination or rate limit ignored")
			}
			if status == 200 && calls != maxPages {
				t.Fatalf("pagination calls: %d", calls)
			}
			if status == 429 {
				failure, ok := errors.AsType[*APIError](err)
				if !ok || failure.RetryAfter != time.Hour || calls != 1 {
					t.Fatalf("rate limit: %v", err)
				}
			}
		})
	}
}

func TestResponseSizeBound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, strings.Repeat("x", maxResponseBytes+1)) }))
	defer server.Close()
	client, _ := NewClient(server.Client(), server.URL, "token")
	if _, err := client.Identity(t.Context()); err == nil {
		t.Fatal("oversized response accepted")
	}
}

func TestConnectionIncludesCanonicalInstance(t *testing.T) {
	first, err := NewClient(http.DefaultClient, "https://GITLAB.example:443", "token")
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := NewClient(http.DefaultClient, "https://gitlab.example", "token")
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewClient(http.DefaultClient, "https://other.example", "token")
	if err != nil {
		t.Fatal(err)
	}
	if first.Connection(42) != repeated.Connection(42) || first.Connection(42) == other.Connection(42) || first.Connection(42) == first.Connection(43) {
		t.Fatal("connection identity is unstable or crosses instances/accounts")
	}
	ipv6, err := NewClient(http.DefaultClient, "https://[::1]:443", "token")
	if err != nil || ipv6.origin != "https://[::1]" {
		t.Fatalf("IPv6 origin: %v %v", ipv6, err)
	}
}

func TestExistingEyesStopsPagination(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("X-Next-Page", "2")
		fmt.Fprint(w, `[{"id":9,"name":"eyes","user":{"id":42}}]`)
	}))
	defer server.Close()
	client, _ := NewClient(server.Client(), server.URL, "token")
	if err := client.reactEyes(t.Context(), "/projects/11/issues/3/notes/1/award_emoji", 42); err != nil || calls != 1 {
		t.Fatalf("existing reaction scanned or posted unnecessarily: calls=%d err=%v", calls, err)
	}
}

func TestUncertainPostPreservesLookupRateLimit(t *testing.T) {
	gets := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(500)
			return
		}
		gets++
		if gets == 1 {
			fmt.Fprint(w, `[]`)
			return
		}
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(429)
	}))
	defer server.Close()
	client, _ := NewClient(server.Client(), server.URL, "token")
	err := client.reactEyes(t.Context(), "/projects/11/issues/3/notes/1/award_emoji", 42)
	failure, ok := errors.AsType[*intake.DeliveryError](err)
	if !ok || failure.Disposition != intake.DeliveryUncertain || failure.RetryScope != intake.RetryConnection || failure.RetryAfter != time.Hour {
		t.Fatalf("lost unknown outcome or account cooldown: %v", err)
	}
}

func TestPollerPreservesLongestRetryDelayAcrossFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/todos" {
			fmt.Fprint(w, `[{"project":{"id":11},"target_type":"Issue","target":{"iid":3}},{"project":{"id":11},"target_type":"Issue","target":{"iid":4}}]`)
			return
		}
		if strings.Contains(r.URL.Path, "/issues/3/") {
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(503)
	}))
	defer server.Close()
	client, _ := NewClient(server.Client(), server.URL, "token")
	policy := testPolicy()
	policy.Connection = client.Connection(42)
	sweep, err := (Poller{Client: client, Policy: policy, Intake: intake.AcceptFunc(func(_ context.Context, _ intake.Submission) (intake.Acceptance, error) {
		t.Fatal("accepted a failed source")
		return intake.Acceptance{}, nil
	})}).Scan(t.Context())
	if err == nil || sweep.NextPoll != time.Hour {
		t.Fatalf("retry delay hidden by earlier failure: %+v %v", sweep, err)
	}
}
