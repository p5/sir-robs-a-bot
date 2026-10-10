package factorytests

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/providers/github"
)

func TestAcceptedMetadataRemainsReadable(t *testing.T) {
	for _, metadata := range []string{`{"n":1e20000}`, `{"n":1e-20000}`, `{"text":"\u0000"}`} {
		t.Run(metadata, func(t *testing.T) {
			fixture := open(t)
			submission := request(t, fixture, 1)
			submission.Metadata = jsontext.Value(metadata)
			accepted, err := fixture.Store.Accept(t.Context(), submission)
			if err != nil {
				t.Fatal(err)
			}
			stored, err := fixture.Store.Get(t.Context(), accepted.RequestID)
			if err != nil || string(stored.Metadata) != metadata {
				t.Fatalf("accepted metadata changed: %s %v", stored.Metadata, err)
			}
		})
	}
}

type reviewRateSender struct{ calls atomic.Int32 }

func (*reviewRateSender) Connection() intake.Connection { return github.Connection(42) }
func (sender *reviewRateSender) Acknowledge(context.Context, intake.Request) error {
	sender.calls.Add(1)
	return &intake.DeliveryError{Err: errors.New("rate limit"), RetryScope: intake.RetryConnection, RetryAfter: time.Hour}
}
func TestAccountRateLimitSurvivesAnotherPass(t *testing.T) {
	fixture := open(t)
	for _, id := range []int64{1, 2} {
		if _, err := fixture.Store.Accept(t.Context(), request(t, fixture, id)); err != nil {
			t.Fatal(err)
		}
	}
	sender := &reviewRateSender{}
	if _, err := fixture.Store.AcknowledgeDue(t.Context(), sender); err == nil {
		t.Fatal("rate limit missing")
	}
	// Reconstruct the store to prove that a fresh delivery pass uses durable state.
	restarted := intake.Store{DB: fixture.Store.DB}
	if _, err := restarted.AcknowledgeDue(t.Context(), sender); err != nil {
		t.Fatal(err)
	}
	if sender.calls.Load() != 1 {
		t.Fatalf("connection cooldown bypassed: calls=%d despite RetryAfter=1h", sender.calls.Load())
	}
}

func TestOneBadGitHubCommentDoesNotBlockLaterCommands(t *testing.T) {
	floor := time.Now().Add(-time.Hour).UTC()
	var accepted atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notifications":
			fmt.Fprintf(w, `[{"repository":{"id":11,"full_name":"p5/test"},"subject":{"type":"Issue","url":"http://%s/repos/p5/test/issues/3"}}]`, r.Host)
		case "/repos/p5/test/issues/3":
			fmt.Fprint(w, `{"id":10,"number":3,"body":"ordinary issue","user":{"id":7,"type":"User"}}`)
		case "/repos/p5/test/issues/3/comments":
			// The first candidate has an editor that cannot be identified as a human.
			fmt.Fprintf(w, `[{"id":1,"node_id":"C1","body":"@sir-robs-a-bot first","user":{"id":7,"type":"User"},"created_at":%q},{"id":2,"node_id":"C2","body":"@sir-robs-a-bot second","user":{"id":7,"type":"User"},"created_at":%q}]`, floor.Add(time.Minute).Format(time.RFC3339), floor.Add(time.Minute).Format(time.RFC3339))
		case "/graphql":
			var payload struct {
				Variables struct {
					ID string `json:"id"`
				} `json:"variables"`
			}
			if err := json.UnmarshalRead(r.Body, &payload); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if payload.Variables.ID == "C1" {
				fmt.Fprint(w, `{"data":{"node":{"id":"C1","__typename":"IssueComment","body":"@sir-robs-a-bot first","author":{"databaseId":7},"editor":{},"lastEditedAt":"2026-10-10T10:00:00Z"}}}`)
				return
			}
			fmt.Fprint(w, `{"data":{"node":{"id":"C2","__typename":"IssueComment","body":"@sir-robs-a-bot second","author":{"databaseId":7},"editor":null}}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client, err := github.NewClient(server.Client(), server.URL, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	poller := github.Poller{Client: client, Policy: github.Policy{Account: github.User{ID: 42, Login: "sir-robs-a-bot"}, AllowedUsers: map[int64]bool{7: true}, Since: floor}, Intake: intake.AcceptFunc(func(_ context.Context, submission intake.Submission) (intake.Acceptance, error) {
		accepted.Add(1)
		return intake.Acceptance{Created: true}, nil
	})}
	for range 2 {
		sweep, err := poller.Scan(t.Context())
		if err == nil || sweep.FailedSources != 1 || sweep.FailedThreads != 1 || sweep.Accepted != 1 {
			t.Fatalf("source failure reporting: %+v %v", sweep, err)
		}
	}
	if accepted.Load() != 2 {
		t.Fatal("bad comment starves later authorized command")
	}
}

func TestUnreadableSnapshotDoesNotBlockOtherReceipts(t *testing.T) {
	fixture := open(t)
	first := request(t, fixture, 1)
	accepted, err := fixture.Store.Accept(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Store.Accept(t.Context(), request(t, fixture, 2)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Store.DB.ExecContext(t.Context(), `UPDATE intake_requests SET snapshot=$2 WHERE id=$1`, accepted.RequestID, []byte(`{"broken":true}`)); err != nil {
		t.Fatal(err)
	}
	sender := &fixtureProvider{connection: github.Connection(42), receipts: make(map[string]bool)}
	count, err := fixture.Store.AcknowledgeDue(t.Context(), sender)
	if err == nil || count != 1 {
		t.Fatalf("quarantine did not isolate record: %d %v", count, err)
	}
	var state string
	if err := fixture.Store.DB.QueryRowContext(t.Context(), `SELECT state FROM intake_acknowledgements WHERE request_id=$1`, accepted.RequestID).Scan(&state); err != nil || state != "quarantined" {
		t.Fatalf("quarantine state: %q %v", state, err)
	}
	if err := fixture.Store.RedriveAcknowledgement(t.Context(), github.Connection(42), accepted.RequestID); err == nil {
		t.Fatal("redrove corrupt snapshot")
	}
}

func TestTerminalReceiptFailureCanStopRetrying(t *testing.T) {
	fixture := open(t)
	accepted, err := fixture.Store.Accept(t.Context(), request(t, fixture, 1))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fixture.Store.AcknowledgeOne(t.Context(), github.Connection(42), func(context.Context, intake.Request) error {
		return &intake.DeliveryError{Err: errors.New("source permanently deleted"), Disposition: intake.DeliveryTerminal}
	})
	var pending bool
	if err := fixture.Store.DB.QueryRowContext(t.Context(), `SELECT state='pending' FROM intake_acknowledgements WHERE request_id=$1`, accepted.RequestID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending {
		t.Fatal("missing terminal receipt state: permanent failure remains pending")
	}
}

func TestAppendOnlyReceiptCannotRecoverUnknownReply(t *testing.T) {
	fixture := open(t)
	submission := request(t, fixture, 1)
	if _, err := fixture.Store.Accept(t.Context(), submission); err != nil {
		t.Fatal(err)
	}
	comments := 0
	sender := func(context.Context, intake.Request) error {
		// Model a successful create-comment call whose response was lost.
		comments++
		if comments == 1 {
			return &intake.DeliveryError{Err: errors.New("lost POST reply"), Disposition: intake.DeliveryUncertain}
		}
		return nil
	}
	_, _ = fixture.Store.AcknowledgeOne(t.Context(), github.Connection(42), sender)
	makeAcknowledgementDue(t, fixture)
	_, err := fixture.Store.AcknowledgeOne(t.Context(), github.Connection(42), sender)
	if err != nil {
		t.Fatal(err)
	}
	if comments != 1 {
		t.Fatalf("append-only receipt duplicated: comments=%d", comments)
	}
}

func TestReceiptModesCreateOnlyRequiredObligations(t *testing.T) {
	for _, mode := range []intake.ReceiptMode{intake.ReceiptAsync, intake.ReceiptInline, intake.ReceiptNone} {
		t.Run(string(mode), func(t *testing.T) {
			fixture := open(t)
			submission := request(t, fixture, 1)
			submission.ReceiptMode = mode
			accepted, err := fixture.Store.Accept(t.Context(), submission)
			if err != nil {
				t.Fatal(err)
			}
			var receipts, queued int
			if err := fixture.Store.DB.QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM intake_acknowledgements),(SELECT count(*) FROM intake_outbox)`).Scan(&receipts, &queued); err != nil {
				t.Fatal(err)
			}
			expected := 0
			if mode == intake.ReceiptAsync {
				expected = 1
			}
			if receipts != expected || queued != 1 {
				t.Fatalf("obligations: receipts=%d queued=%d", receipts, queued)
			}
			// Changing policy on a duplicate must not alter the accepted snapshot.
			submission.ReceiptMode = intake.ReceiptNone
			duplicate, err := fixture.Store.Accept(t.Context(), submission)
			if err != nil || duplicate.Created {
				t.Fatalf("duplicate: %+v %v", duplicate, err)
			}
			stored, err := fixture.Store.Get(t.Context(), accepted.RequestID)
			if err != nil || stored.ReceiptMode != mode {
				t.Fatalf("receipt policy changed: %+v %v", stored, err)
			}
		})
	}
}

func TestReceiptRedriveScopesRecoveryAndPreservesCooldown(t *testing.T) {
	fixture := open(t)
	accepted, err := fixture.Store.Accept(t.Context(), request(t, fixture, 1))
	if err != nil {
		t.Fatal(err)
	}
	connection := github.Connection(42)
	if _, err := fixture.Store.AcknowledgeOne(t.Context(), connection, func(context.Context, intake.Request) error {
		return &intake.DeliveryError{Disposition: intake.DeliveryTerminal, RetryScope: intake.RetryConnection, RetryAfter: time.Hour}
	}); err == nil {
		t.Fatal("missing terminal error")
	}
	status, err := fixture.Store.Acknowledgement(t.Context(), connection, accepted.RequestID)
	if err != nil || status.State != "terminal" || status.Attempts != 1 || status.ConnectionNotBefore == nil {
		t.Fatalf("terminal status: %+v %v", status, err)
	}
	if err := fixture.Store.RedriveAcknowledgement(t.Context(), github.Connection(99), accepted.RequestID); err == nil {
		t.Fatal("cross-account redrive succeeded")
	}
	if _, err := fixture.Store.Acknowledgement(t.Context(), github.Connection(99), accepted.RequestID); err == nil {
		t.Fatal("cross-account inspection succeeded")
	}
	if err := fixture.Store.RedriveAcknowledgement(t.Context(), connection, accepted.RequestID); err != nil {
		t.Fatal(err)
	}
	if found, err := fixture.Store.AcknowledgeOne(t.Context(), connection, func(context.Context, intake.Request) error { t.Error("redrive bypassed cooldown"); return nil }); err != nil || found {
		t.Fatalf("cooldown: %v %v", found, err)
	}
	if _, err := fixture.Store.DB.ExecContext(t.Context(), `UPDATE intake_connections SET acknowledgement_not_before=NULL`); err != nil {
		t.Fatal(err)
	}
	if found, err := fixture.Store.AcknowledgeOne(t.Context(), connection, func(context.Context, intake.Request) error { return nil }); err != nil || !found {
		t.Fatalf("redrive delivery: %v %v", found, err)
	}
	if err := fixture.Store.RedriveAcknowledgement(t.Context(), connection, accepted.RequestID); err == nil {
		t.Fatal("completed receipt redriven")
	}
}

func TestConnectionReceiptLockDoesNotBlockAcceptance(t *testing.T) {
	fixture := open(t)
	if _, err := fixture.Store.Accept(t.Context(), request(t, fixture, 1)); err != nil {
		t.Fatal(err)
	}
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := fixture.Store.AcknowledgeOne(t.Context(), github.Connection(42), func(context.Context, intake.Request) error {
			close(started)
			<-release
			return &intake.DeliveryError{RetryScope: intake.RetryConnection, RetryAfter: time.Hour}
		})
		finished <- err
	}()
	<-started
	defer func() { close(release); <-finished }()
	if found, err := fixture.Store.AcknowledgeOne(t.Context(), github.Connection(42), func(context.Context, intake.Request) error { t.Error("concurrent sender called"); return nil }); err != nil || found {
		t.Fatalf("concurrent claim: %v %v", found, err)
	}
	acceptance, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := fixture.Store.Accept(acceptance, request(t, fixture, 2)); err != nil {
		t.Fatalf("receipt delivery blocked acceptance: %v", err)
	}
}

func TestReceiptRecoveryCLI(t *testing.T) {
	fixture := open(t)
	accepted, err := fixture.Store.Accept(t.Context(), request(t, fixture, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Store.AcknowledgeOne(t.Context(), github.Connection(42), func(context.Context, intake.Request) error {
		return &intake.DeliveryError{Disposition: intake.DeliveryTerminal}
	}); err == nil {
		t.Fatal("missing delivery failure")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" || r.Method != "GET" {
			t.Errorf("recovery made unexpected API call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		fmt.Fprint(w, `{"id":42,"login":"sir-robs-a-bot","type":"User"}`)
	}))
	defer server.Close()
	output := runCLI(t, fixture, server.URL, "receipt", accepted.RequestID)
	var status intake.AcknowledgementStatus
	if err := json.Unmarshal([]byte(output), &status); err != nil || status.State != "terminal" {
		t.Fatalf("CLI receipt: %s %v", output, err)
	}
	runCLI(t, fixture, server.URL, "redrive-ack", accepted.RequestID)
	status, err = fixture.Store.Acknowledgement(t.Context(), github.Connection(42), accepted.RequestID)
	if err != nil || status.State != "pending" || status.Attempts != 1 {
		t.Fatalf("CLI redrive: %+v %v", status, err)
	}
}

func TestMigrationRefusesRewritingSnapshotSchema(t *testing.T) {
	fixture := open(t)
	if _, err := fixture.Store.DB.ExecContext(t.Context(), `ALTER TABLE intake_requests ALTER COLUMN snapshot TYPE jsonb USING convert_from(snapshot,'UTF8')::jsonb`); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Store.Migrate(t.Context()); err == nil {
		t.Fatal("legacy snapshot schema silently accepted")
	}
}
