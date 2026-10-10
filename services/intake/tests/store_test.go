package factorytests

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/providers/github"
)

func request(t *testing.T, fixture fixture, id int64) intake.Submission {
	t.Helper()
	policy := github.Policy{Account: github.User{ID: 42, Login: "sir-robs-a-bot"}, AllowedUsers: map[int64]bool{7: true}, Since: fixture.Since}
	accepted, ok, err := policy.Candidate(github.Repository{ID: 11, FullName: "p5/test"}, "comment", github.Source{
		ID: id, Number: 3, Body: "@sir-robs-a-bot explain this", User: github.User{ID: 7, Login: "p5", Type: "User"}, CreatedAt: fixture.Since.Add(time.Minute),
	})
	if err != nil || !ok {
		t.Fatalf("request fixture: %v %v", ok, err)
	}
	accepted.SubmittedBy = accepted.Author
	return accepted
}

func TestConcurrentAcceptancePreservesFirstSnapshot(t *testing.T) {
	fixture := open(t)
	accepted := request(t, fixture, 1)
	var inserted atomic.Int32
	var group sync.WaitGroup
	for range 24 {
		group.Go(func() {
			fresh, err := fixture.Store.Accept(t.Context(), accepted)
			if err != nil {
				t.Error(err)
				return
			}
			if fresh.Created {
				inserted.Add(1)
			}
		})
	}
	group.Wait()
	if inserted.Load() != 1 {
		t.Fatalf("inserted %d requests", inserted.Load())
	}
	var outbox int
	if err := fixture.Store.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM intake_outbox`).Scan(&outbox); err != nil || outbox != 1 {
		t.Fatalf("outbox %d: %v", outbox, err)
	}
	accepted.Body = "@sir-robs-a-bot replace the original request"
	accepted.Instruction = "replace the original request"
	if fresh, err := fixture.Store.Accept(t.Context(), accepted); err != nil || fresh.Created {
		t.Fatalf("edited source: %v %v", fresh, err)
	}
	saved, err := fixture.Store.Get(t.Context(), submissionID(t, accepted))
	if err != nil || saved.Instruction != "explain this" {
		t.Fatalf("snapshot changed: %+v %v", saved, err)
	}
	if err := fixture.Store.Activate(t.Context(), github.Connection(42), fixture.Since.Add(-time.Hour)); err == nil {
		t.Fatal("activation floor silently changed")
	}
}

func TestOutboxFailureRollsBackRequest(t *testing.T) {
	fixture := open(t)
	_, err := fixture.Store.DB.ExecContext(t.Context(), `CREATE FUNCTION reject_outbox() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected outbox failure'; END $$;
 CREATE TRIGGER reject_outbox BEFORE INSERT ON intake_outbox FOR EACH ROW EXECUTE FUNCTION reject_outbox()`)
	if err != nil {
		t.Fatal(err)
	}
	accepted := request(t, fixture, 2)
	if _, err := fixture.Store.Accept(t.Context(), accepted); err == nil {
		t.Fatal("injected failure accepted")
	}
	if _, err := fixture.Store.Get(t.Context(), submissionID(t, accepted)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("request survived failed obligation: %v", err)
	}
	if _, err := fixture.Store.DB.ExecContext(t.Context(), `DROP TRIGGER reject_outbox ON intake_outbox`); err != nil {
		t.Fatal(err)
	}
	if fresh, err := fixture.Store.Accept(t.Context(), accepted); err != nil || !fresh.Created {
		t.Fatalf("recovery: %v %v", fresh, err)
	}
}

type loseEnqueueReply struct{ datastore.Store }

func (queue loseEnqueueReply) Enqueue(ctx context.Context, key datastore.Key, options ...datastore.EnqueueOptions) error {
	if err := queue.Store.Enqueue(ctx, key, options...); err != nil {
		return err
	}
	return errors.New("injected lost enqueue reply")
}

func TestOutboxSurvivesUnknownReplyAndReopen(t *testing.T) {
	fixture := open(t)
	accepted := request(t, fixture, 3)
	if _, err := fixture.Store.Accept(t.Context(), accepted); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Store.DeliverOne(t.Context(), loseEnqueueReply{fixture.Queue}); err == nil {
		t.Fatal("lost reply not surfaced")
	}
	db, err := sql.Open("postgres", fixture.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reopened := intake.Store{DB: db}
	if found, err := reopened.DeliverOne(t.Context(), fixture.Queue); err != nil || !found {
		t.Fatalf("recovery: %v %v", found, err)
	}
	if found, err := reopened.DeliverOne(t.Context(), fixture.Queue); err != nil || found {
		t.Fatalf("outbox not drained: %v %v", found, err)
	}
	if _, err := fixture.Store.Accept(t.Context(), accepted); err != nil {
		t.Fatal(err)
	}
	if found, err := reopened.DeliverOne(t.Context(), fixture.Queue); err != nil || found {
		t.Fatalf("replay recreated obligation: %v %v", found, err)
	}
	item, err := fixture.Queue.Get(t.Context(), datastore.Key{Kind: intake.Kind, ID: submissionID(t, accepted)})
	if err != nil || !item.Pending {
		t.Fatalf("queue item missing: %+v %v", item, err)
	}
}

func TestActivationNormalizesTimestampPrecision(t *testing.T) {
	fixture := open(t)
	since := fixture.Since.Add(987654321 * time.Nanosecond)
	for range 2 {
		if err := fixture.Store.Activate(t.Context(), github.Connection(43), since); err != nil {
			t.Fatalf("fractional activation: %v", err)
		}
	}
	saved, err := fixture.Store.Activation(t.Context(), github.Connection(43))
	if err != nil || !saved.Equal(since.Truncate(time.Microsecond)) {
		t.Fatalf("stored activation: %v %v", saved, err)
	}
}
