// Command request-run demonstrates durable-operation ordering with ephemeral stores.
package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile"
	queue "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	queuememory "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/memory"
	"github.com/p5/sir-robs-a-bot/packages/resources"
	contentmemory "github.com/p5/sir-robs-a-bot/packages/resources/content/memory"
	statememory "github.com/p5/sir-robs-a-bot/packages/resources/datastore/memory"
	example "github.com/p5/sir-robs-a-bot/packages/resources/examples/request-run"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) != 2 || args[0] != "demo" {
		return errors.New("usage: request-run demo INSTRUCTION; all demo stores are ephemeral")
	}
	repository, err := resources.New(contentmemory.New(), statememory.New())
	if err != nil {
		return err
	}
	runs, err := resources.New(contentmemory.New(), statememory.New())
	if err != nil {
		return err
	}
	app := example.Application{Requests: repository, Runs: runs}
	if _, _, err := app.Submit(ctx, "example", args[1]); err != nil {
		return err
	}
	target := queuememory.New(queuememory.Config{})
	engine, err := reconcile.New(target, map[string]reconcile.Reconciler{
		example.RequestKind: reconcile.ReconcilerFunc(app.RequestReconciler),
		example.RunKind:     reconcile.ReconcilerFunc(app.RunReconciler),
	}, reconcile.Config{Concurrency: 1, LeaseDuration: time.Minute, CallTimeout: 10 * time.Second,
		PollInterval: time.Second, RetryInitial: time.Second, RetryMax: time.Minute, MaxFailures: 3})
	if err != nil {
		return err
	}
	for range 20 {
		for _, owner := range []*resources.Repository{repository, runs} {
			for {
				found, err := owner.DeliverOne(ctx, target)
				if err != nil {
					return err
				}
				if !found {
					break
				}
			}
		}
		found, err := engine.ReconcileOne(ctx)
		if err != nil {
			return err
		}
		if !found {
			record, _, err := runs.Get(ctx, queue.Key{Kind: example.RunKind, ID: "example"})
			if err != nil {
				return err
			}
			var state example.State
			if json.Unmarshal(record.State, &state) != nil || state.Phase != "complete" {
				return errors.New("prototype stopped before terminal progress")
			}
			return json.MarshalWrite(os.Stdout, record)
		}
	}
	return errors.New("prototype failed to converge within 20 calls")
}
