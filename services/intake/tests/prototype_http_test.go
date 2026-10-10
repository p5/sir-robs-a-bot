package factorytests

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPrototypeHTTPAcceptanceReturnsDurableReceipt(t *testing.T) {
	fixture := open(t)
	connection := intake.Connection{Provider: "http", Account: "tenant-one"}
	if err := fixture.Store.Activate(t.Context(), connection, fixture.Since); err != nil {
		t.Fatal(err)
	}
	// The host supplies the verified principal; the body cannot choose a tenant.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			w.WriteHeader(401)
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			w.WriteHeader(400)
			return
		}
		var input struct{ Instruction string }
		if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, 16384), &input); err != nil {
			w.WriteHeader(400)
			return
		}
		accepted, err := fixture.Store.Accept(r.Context(), intake.Submission{ReceiptMode: intake.ReceiptInline, Source: intake.Source{Connection: connection, Scope: "requests", Kind: "command", ID: key}, Author: "principal-one", SubmittedBy: "principal-one", Body: input.Instruction, Instruction: input.Instruction, CreatedAt: time.Now().UTC(), ObservedAt: time.Now().UTC(), Metadata: jsontext.Value(`{}`)})
		if err != nil {
			w.WriteHeader(503)
			return
		}
		output, err := json.Marshal(accepted)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		w.Write(output)
	})
	first := ""
	for range 2 {
		request := httptest.NewRequest("POST", "/requests", strings.NewReader(`{"Instruction":"investigate"}`))
		request.Header.Set("Authorization", "Bearer fixture-token")
		request.Header.Set("Idempotency-Key", "request-one")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var receipt intake.Acceptance
		if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil || response.Code != 202 || receipt.RequestID == "" {
			t.Fatalf("HTTP acceptance: %d %s %v", response.Code, response.Body, err)
		}
		if first != "" && (receipt.Created || receipt.RequestID != first) {
			t.Fatal("HTTP retry duplicated request")
		}
		first = receipt.RequestID
	}
	if found, err := fixture.Store.DeliverOne(t.Context(), fixture.Queue); err != nil || !found {
		t.Fatalf("HTTP queue obligation: %v %v", found, err)
	}
	item, err := fixture.Queue.Get(t.Context(), datastore.Key{Kind: intake.Kind, ID: first})
	if err != nil || !item.Pending {
		t.Fatalf("missing HTTP request: %+v %v", item, err)
	}
	var pending int
	if err := fixture.Store.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM intake_acknowledgements WHERE delivered_at IS NULL`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	// The durable HTTP response is the receipt; no later send is needed.
	if pending != 0 {
		t.Fatalf("unexpected async receipt policy: %d", pending)
	}
}
