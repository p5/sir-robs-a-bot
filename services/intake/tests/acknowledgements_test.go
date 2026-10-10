package factorytests

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/providers/github"
)

func makeAcknowledgementDue(t *testing.T, fixture fixture) {
	t.Helper()
	if _, err := fixture.Store.DB.ExecContext(t.Context(), `UPDATE intake_acknowledgements SET next_attempt_at=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
}

func TestAcknowledgementFailureDoesNotBlockQueueOrOtherRequests(t *testing.T) {
	fixture := open(t)
	for _, id := range []int64{1, 2} {
		if _, err := fixture.Store.Accept(t.Context(), request(t, fixture, id)); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/comments/1/") {
			w.WriteHeader(403)
			return
		}
		fmt.Fprint(w, `{"id":1,"content":"eyes","user":{"id":42}}`)
	}))
	defer server.Close()
	client, _ := github.NewClient(server.Client(), server.URL, "test")
	count, err := fixture.Store.AcknowledgeDue(t.Context(), github.Acknowledger{Client: client, AccountID: 42})
	if err == nil || count != 1 {
		t.Fatalf("failure isolation: %d %v", count, err)
	}
	if found, err := fixture.Store.DeliverOne(t.Context(), fixture.Queue); err != nil || !found {
		t.Fatalf("queue blocked: %v %v", found, err)
	}
	var attempts int
	var future bool
	if err := fixture.Store.DB.QueryRowContext(t.Context(), `SELECT attempts,next_attempt_at>clock_timestamp() FROM intake_acknowledgements WHERE request_id=$1`, submissionID(t, request(t, fixture, 1))).Scan(&attempts, &future); err != nil || attempts != 1 || !future {
		t.Fatalf("retry not scheduled: %d %v %v", attempts, future, err)
	}
}

func TestAcknowledgementLostReplyRequiresExplicitRedriveWithoutDuplicateReaction(t *testing.T) {
	fixture := open(t)
	accepted := request(t, fixture, 1)
	if _, err := fixture.Store.Accept(t.Context(), accepted); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var reactions atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" {
			fmt.Fprint(w, `{"id":42,"login":"sir-robs-a-bot","type":"User"}`)
			return
		}
		if r.URL.Path != "/repos/p5/test/issues/comments/1/reactions" || r.Method != "POST" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		if calls.Add(1) == 1 {
			reactions.Add(1)
			// Simulate GitHub committing a reaction before the HTTP reply is lost.
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			connection.Close()
			return
		}
		fmt.Fprint(w, `{"id":80,"content":"eyes","user":{"id":42}}`)
	}))
	defer server.Close()
	output, err := command(t, fixture, server.URL, "acknowledge").CombinedOutput()
	if err == nil {
		t.Fatalf("lost reply not surfaced: %s", output)
	}
	makeAcknowledgementDue(t, fixture)
	if got := runCLI(t, fixture, server.URL, "acknowledge"); !strings.Contains(got, `"Acknowledged":0`) || calls.Load() != 1 {
		t.Fatalf("uncertain delivery retried automatically: %s calls=%d", got, calls.Load())
	}
	id := submissionID(t, accepted)
	status, err := fixture.Store.Acknowledgement(t.Context(), github.Connection(42), id)
	if err != nil || status.State != "uncertain" {
		t.Fatalf("lost reply did not stop receipt: %+v %v", status, err)
	}
	// The fixture represents an operator confirming the existing remote reaction
	// before explicitly resuming the legacy PostgreSQL delivery operation.
	runCLI(t, fixture, server.URL, "redrive-ack", id)
	if got := runCLI(t, fixture, server.URL, "acknowledge"); !strings.Contains(got, `"Acknowledged":1`) {
		t.Fatalf("recovery: %s", got)
	}
	if _, err := fixture.Store.Accept(t.Context(), accepted); err != nil {
		t.Fatal(err)
	}
	if got := runCLI(t, fixture, server.URL, "acknowledge"); !strings.Contains(got, `"Acknowledged":0`) {
		t.Fatalf("replay: %s", got)
	}
	if calls.Load() != 2 || reactions.Load() != 1 {
		t.Fatalf("calls=%d reactions=%d", calls.Load(), reactions.Load())
	}
}

func TestAcknowledgementsAreAccountScopedAndConcurrent(t *testing.T) {
	fixture := open(t)
	if _, err := fixture.Store.Accept(t.Context(), request(t, fixture, 1)); err != nil {
		t.Fatal(err)
	}
	sender := func(context.Context, intake.Request) error { t.Error("wrong account delivered"); return nil }
	if found, err := fixture.Store.AcknowledgeOne(t.Context(), github.Connection(43), sender); err != nil || found {
		t.Fatalf("account isolation: %v %v", found, err)
	}
	var calls atomic.Int32
	var group sync.WaitGroup
	for range 12 {
		group.Go(func() {
			_, err := fixture.Store.AcknowledgeOne(t.Context(), github.Connection(42), func(context.Context, intake.Request) error { calls.Add(1); return nil })
			if err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent deliveries: %d", calls.Load())
	}
}

func TestAcknowledgementInsertionFailureRollsBackAcceptance(t *testing.T) {
	fixture := open(t)
	_, err := fixture.Store.DB.ExecContext(t.Context(), `CREATE FUNCTION reject_ack() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure'; END $$;
CREATE TRIGGER reject_ack BEFORE INSERT ON intake_acknowledgements FOR EACH ROW EXECUTE FUNCTION reject_ack()`)
	if err != nil {
		t.Fatal(err)
	}
	accepted := request(t, fixture, 1)
	if _, err := fixture.Store.Accept(t.Context(), accepted); err == nil {
		t.Fatal("failed obligation accepted")
	}
	if _, err := fixture.Store.Get(t.Context(), submissionID(t, accepted)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("request survived: %v", err)
	}
	var pending int
	if err := fixture.Store.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM intake_outbox`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("queue obligation survived: %d %v", pending, err)
	}
}

func TestAcknowledgementRateLimitStopsPass(t *testing.T) {
	fixture := open(t)
	for _, id := range []int64{1, 2} {
		if _, err := fixture.Store.Accept(t.Context(), request(t, fixture, id)); err != nil {
			t.Fatal(err)
		}
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "123")
		w.WriteHeader(429)
	}))
	defer server.Close()
	client, _ := github.NewClient(server.Client(), server.URL, "test")
	if count, err := fixture.Store.AcknowledgeDue(t.Context(), github.Acknowledger{Client: client, AccountID: 42}); err == nil || count != 0 || calls.Load() != 1 {
		t.Fatalf("rate limit: %d %v calls=%d", count, err, calls.Load())
	}
	var next time.Time
	if err := fixture.Store.DB.QueryRowContext(t.Context(), `SELECT max(next_attempt_at) FROM intake_acknowledgements`).Scan(&next); err != nil || time.Until(next) < 120*time.Second {
		t.Fatalf("retry delay: %v %v", next, err)
	}
}
