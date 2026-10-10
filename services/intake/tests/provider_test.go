package factorytests

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
)

// fixtureProvider proves the integration boundary without pretending to be a
// working Slack adapter. No provider-specific types enter the intake store.
type fixtureProvider struct {
	connection intake.Connection
	receipts   map[string]bool
	loseReply  bool
}

func (provider *fixtureProvider) Connection() intake.Connection { return provider.connection }

func (provider *fixtureProvider) submit(ctx context.Context, acceptor intake.Acceptor, since time.Time) (intake.Acceptance, error) {
	// A real adapter verifies the event and its actor before this call.
	return acceptor.Accept(ctx, intake.Submission{ReceiptMode: intake.ReceiptAsync,
		Source: intake.Source{Connection: provider.connection, Scope: "C/channel", Kind: "message", ID: "1728561600.000123"},
		Author: "U-author", SubmittedBy: "U-editor", Body: "investigate", Instruction: "investigate", CreatedAt: since.Add(time.Minute), ObservedAt: time.Now().UTC(),
		Metadata: jsontext.Value(`{"channel":"C/channel","thread":"1728561600.000123"}`),
	})
}

func (provider *fixtureProvider) Acknowledge(_ context.Context, request intake.Request) error {
	if request.Source.Connection != provider.connection {
		return errors.New("wrong provider connection")
	}
	provider.receipts[request.ID] = true
	if provider.loseReply {
		provider.loseReply = false
		return &intake.DeliveryError{Err: errors.New("lost receipt reply"), RetryAfter: time.Minute}
	}
	return nil
}

func TestSecondProviderUsesAcceptanceQueueAndAcknowledgementContracts(t *testing.T) {
	fixture := open(t)
	provider := &fixtureProvider{connection: intake.Connection{Provider: "fixture", Account: "T-workspace"}, receipts: make(map[string]bool), loseReply: true}
	if err := fixture.Store.Activate(t.Context(), provider.Connection(), fixture.Since); err != nil {
		t.Fatal(err)
	}
	first, err := provider.submit(t.Context(), fixture.Store, fixture.Since)
	if err != nil || !first.Created {
		t.Fatalf("accept: %+v %v", first, err)
	}
	duplicate, err := provider.submit(t.Context(), fixture.Store, fixture.Since)
	if err != nil || duplicate.Created || duplicate.RequestID != first.RequestID {
		t.Fatalf("deduplication: %+v %v", duplicate, err)
	}
	saved, err := fixture.Store.Get(t.Context(), first.RequestID)
	if err != nil || saved.Author != "U-author" || saved.SubmittedBy != "U-editor" {
		t.Fatalf("provider snapshot: %+v %v", saved, err)
	}
	var metadata struct {
		Channel string `json:"channel"`
		Thread  string `json:"thread"`
	}
	if err := json.Unmarshal(saved.Metadata, &metadata); err != nil || metadata.Channel != "C/channel" || metadata.Thread != "1728561600.000123" {
		t.Fatalf("provider metadata: %+v %v", metadata, err)
	}
	if found, err := fixture.Store.DeliverOne(t.Context(), fixture.Queue); err != nil || !found {
		t.Fatalf("queue delivery: %v %v", found, err)
	}
	item, err := fixture.Queue.Get(t.Context(), datastore.Key{Kind: intake.Kind, ID: first.RequestID})
	if err != nil || !item.Pending {
		t.Fatalf("missing queue item: %+v %v", item, err)
	}
	if _, err := fixture.Store.AcknowledgeDue(t.Context(), provider); err == nil {
		t.Fatal("lost reply not surfaced")
	}
	makeAcknowledgementDue(t, fixture)
	if count, err := fixture.Store.AcknowledgeDue(t.Context(), provider); err != nil || count != 1 {
		t.Fatalf("receipt recovery: %d %v", count, err)
	}
	if count, err := fixture.Store.AcknowledgeDue(t.Context(), provider); err != nil || count != 0 || len(provider.receipts) != 1 {
		t.Fatalf("duplicate receipt: %d %v", count, err)
	}
	for _, connection := range []intake.Connection{{Provider: "other", Account: "T-workspace"}, {Provider: "fixture", Account: "other-workspace"}} {
		if err := fixture.Store.Activate(t.Context(), connection, fixture.Since); err != nil {
			t.Fatal(err)
		}
		other := &fixtureProvider{connection: connection, receipts: make(map[string]bool)}
		result, err := other.submit(t.Context(), fixture.Store, fixture.Since)
		if err != nil || !result.Created || result.RequestID == first.RequestID {
			t.Fatalf("connection isolation: %+v %v", result, err)
		}
		ids, err := fixture.Store.List(t.Context(), connection)
		if err != nil || len(ids) != 1 || ids[0] != result.RequestID {
			t.Fatalf("listing isolation: %v %v", ids, err)
		}
		if count, err := fixture.Store.AcknowledgeDue(t.Context(), other); err != nil || count != 1 {
			t.Fatalf("acknowledgement isolation: %d %v", count, err)
		}
	}
}

var _ intake.Acknowledger = (*fixtureProvider)(nil)

func TestLegacySchemaRequiresExplicitMigration(t *testing.T) {
	fixture := open(t)
	if _, err := fixture.Store.DB.ExecContext(t.Context(), `CREATE TABLE github_ingress_requests(id text PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Store.Migrate(t.Context()); err == nil {
		t.Fatal("legacy application state silently ignored")
	}
}

func TestProviderMetadataNearSizeLimitSurvivesPersistence(t *testing.T) {
	fixture := open(t)
	submission := request(t, fixture, 9)
	submission.Metadata = jsontext.Value("[" + strings.Repeat("0,", 7000) + "0]")
	accepted, err := fixture.Store.Accept(t.Context(), submission)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := fixture.Store.Get(t.Context(), accepted.RequestID)
	if err != nil || saved.Validate() != nil {
		t.Fatalf("database formatting invalidated metadata: %v", err)
	}
}
