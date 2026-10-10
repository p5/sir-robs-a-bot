// Package requestrun proves immutable request inputs and mutable run progress.
// It captures an instruction digest; it does not execute an agent or workload.
package requestrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"strings"

	"github.com/p5/sir-robs-a-bot/packages/reconcile"
	queue "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/resources"
	"github.com/p5/sir-robs-a-bot/packages/resources/datastore"
)

const (
	RequestKind = "prototype-request"
	RunKind     = "prototype-run"
)

type Request struct{ Instruction string }
type Run struct{ Request queue.Key }
type State struct {
	Phase             string
	InstructionSHA256 string `json:",omitempty"`
}

type Application struct {
	Requests *resources.Repository
	Runs     *resources.Repository
}

func (app Application) Submit(ctx context.Context, id, instruction string) (datastore.Record, bool, error) {
	if strings.TrimSpace(instruction) == "" || len(instruction) > 64<<10 {
		return datastore.Record{}, false, errors.New("instruction is required and limited to 64 KiB")
	}
	data, err := json.Marshal(Request{Instruction: instruction})
	if err != nil {
		return datastore.Record{}, false, err
	}
	return app.Requests.Create(ctx, queue.Key{Kind: RequestKind, ID: id}, data, []byte(`{"Phase":"accepted"}`))
}

// RequestReconciler durably creates a run before recording the handoff. If the
// handoff update fails, retry finds the same run using the stable request ID.
func (app Application) RequestReconciler(ctx context.Context, key queue.Key) (reconcile.Result, error) {
	record, _, err := app.Requests.Get(ctx, key)
	if err != nil {
		return reconcile.Result{}, err
	}
	var state State
	if json.Unmarshal(record.State, &state) != nil {
		return reconcile.Result{}, reconcile.Permanent(errors.New("invalid request state"))
	}
	if state.Phase == "routed" {
		return reconcile.Result{}, nil
	}
	if state.Phase != "accepted" {
		return reconcile.Result{}, reconcile.Permanent(errors.New("unknown request phase"))
	}
	data, err := json.Marshal(Run{Request: key})
	if err != nil {
		return reconcile.Result{}, err
	}
	if _, _, err := app.Runs.Create(ctx, queue.Key{Kind: RunKind, ID: key.ID}, data, []byte(`{"Phase":"pending"}`)); err != nil {
		return reconcile.Result{}, err
	}
	_, err = app.Requests.Update(ctx, key, record.Version, []byte(`{"Phase":"routed"}`))
	return reconcile.Result{}, err
}

func (app Application) RunReconciler(ctx context.Context, key queue.Key) (reconcile.Result, error) {
	record, data, err := app.Runs.Get(ctx, key)
	if err != nil {
		return reconcile.Result{}, err
	}
	var state State
	var run Run
	if json.Unmarshal(record.State, &state) != nil || json.Unmarshal(data, &run) != nil || run.Request.Kind != RequestKind {
		return reconcile.Result{}, reconcile.Permanent(errors.New("invalid run"))
	}
	if state.Phase == "complete" {
		return reconcile.Result{}, nil
	}
	if state.Phase != "pending" {
		return reconcile.Result{}, reconcile.Permanent(errors.New("unknown run phase"))
	}
	_, input, err := app.Requests.Get(ctx, run.Request)
	if err != nil {
		return reconcile.Result{}, err
	}
	var request Request
	if json.Unmarshal(input, &request) != nil || request.Instruction == "" {
		return reconcile.Result{}, reconcile.Permanent(errors.New("invalid request input"))
	}
	digest := sha256.Sum256([]byte(request.Instruction))
	next, err := json.Marshal(State{Phase: "complete", InstructionSHA256: hex.EncodeToString(digest[:])})
	if err != nil {
		return reconcile.Result{}, err
	}
	_, err = app.Runs.Update(ctx, key, record.Version, next)
	return reconcile.Result{}, err
}
