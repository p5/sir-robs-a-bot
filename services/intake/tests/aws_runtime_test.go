package factorytests

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/providers/github"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/providers/gitlab"
)

func TestAWSExecutableProviders(t *testing.T) {
	aws := openAWS(t)
	for _, provider := range []string{"github", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			fixture := aws
			fixture.Config.FactoryNamespace += "-" + provider
			fixture.Config.OwnerNamespace = fixture.Config.FactoryNamespace + "-intake-work"
			fixture.Config.ResourceNamespace = fixture.Config.FactoryNamespace + "-intake-resources"
			floor := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
			var edited atomic.Bool
			server := testAPI(t, floor, &edited, nil)
			connection := github.Connection(42)
			if provider == "gitlab" {
				server = gitlabAPI(t, floor)
				client, err := gitlab.NewClient(server.Client(), server.URL, "test-secret")
				if err != nil {
					t.Fatal(err)
				}
				connection = client.Connection(42)
			}
			fixture.cli(t, server.URL, "-provider", provider, "-since", floor.Format(time.RFC3339), "activate")
			fixture.cli(t, server.URL, "-provider", provider, "scan")
			store := fixture.open(t)
			ids, err := store.List(t.Context(), connection)
			if err != nil || len(ids) == 0 {
				t.Fatalf("AWS provider requests: %v %v", ids, err)
			}
			for _, id := range ids {
				input, err := store.Get(t.Context(), id)
				if err != nil || input.Validate() != nil {
					t.Fatalf("AWS input: %+v %v", input, err)
				}
				status, err := store.Acknowledgement(t.Context(), connection, id)
				if err != nil || status.State != "delivered" || status.Attempts != 1 {
					t.Fatalf("AWS receipt: %+v %v", status, err)
				}
				item, err := store.Factory.Get(t.Context(), datastore.Key{Kind: intake.Kind, ID: id})
				if err != nil || !item.Pending {
					t.Fatalf("factory handoff missing: %+v %v", item, err)
				}
				fixture.cli(t, server.URL, "-provider", provider, "inspect", id)
				fixture.cli(t, server.URL, "-provider", provider, "receipt", id)
			}
			fixture.cli(t, server.URL, "-provider", provider, "scan")
			status, err := fixture.open(t).Acknowledgement(t.Context(), connection, ids[0])
			if err != nil || status.Attempts != 1 {
				t.Fatalf("duplicate scan repeated receipt: %+v %v", status, err)
			}
		})
	}
}

type crashingSender struct {
	connection intake.Connection
	calls      *atomic.Int64
	cancel     context.CancelFunc
}

func (sender crashingSender) Connection() intake.Connection { return sender.connection }
func (sender crashingSender) Acknowledge(context.Context, intake.Request) error {
	sender.calls.Add(1)
	if sender.cancel != nil {
		sender.cancel()
		return context.Canceled
	}
	return nil
}

func TestAWSInterruptedReceiptRecoversWithoutBlockingOtherWork(t *testing.T) {
	fixture := openAWS(t)
	store := fixture.open(t)
	source := open(t)
	first := request(t, source, 401)
	if err := store.Activate(t.Context(), first.Source.Connection, source.Since); err != nil {
		t.Fatal(err)
	}
	accepted, err := store.Accept(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	interrupted, cancel := context.WithCancel(t.Context())
	sender := crashingSender{connection: first.Source.Connection, calls: &calls, cancel: cancel}
	if _, err := store.Drain(interrupted, sender); !errors.Is(err, context.Canceled) {
		t.Fatalf("interruption not observed: %v", err)
	}
	sender.cancel = nil
	// A new host and SDK clients share only durable resources and queue records.
	store = fixture.open(t)
	_, statusErr := store.Acknowledgement(t.Context(), sender.Connection(), accepted.RequestID)
	if statusErr != nil {
		t.Fatal(statusErr)
	}
	// The real engine persists callback failure with a retry deadline. Wait for
	// observable recovery, bounded by a deadline, rather than assuming scheduling.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := store.Drain(t.Context(), sender); err != nil {
			t.Fatal(err)
		}
		status, err := store.Acknowledgement(t.Context(), sender.Connection(), accepted.RequestID)
		if err != nil {
			t.Fatal(err)
		}
		if status.State == "uncertain" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("receipt did not recover: %+v", status)
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("interruption repeated provider call: %d", calls.Load())
	}
	second := first
	second.Source.ID = "402"
	next, err := store.Accept(t.Context(), second)
	if err != nil {
		t.Fatal(err)
	}
	sender.cancel = nil
	if _, err := store.Drain(t.Context(), sender); err != nil {
		t.Fatal(err)
	}
	status, err := store.Acknowledgement(t.Context(), sender.Connection(), next.RequestID)
	if err != nil || status.State != "delivered" {
		t.Fatalf("uncertain receipt blocked another: %+v %v", status, err)
	}
	factory, err := store.Factory.Get(t.Context(), datastore.Key{Kind: intake.Kind, ID: next.RequestID})
	if err != nil || !factory.Pending {
		t.Fatalf("uncertain receipt blocked factory: %+v %v", factory, err)
	}
}

func TestAWSWorkerNeedsNoProviderCredential(t *testing.T) {
	fixture := openAWS(t)
	store := fixture.open(t)
	source := open(t)
	submission := request(t, source, 701)
	if err := store.Activate(t.Context(), submission.Source.Connection, source.Since); err != nil {
		t.Fatal(err)
	}
	accepted, err := store.Accept(t.Context(), submission)
	if err != nil {
		t.Fatal(err)
	}
	process := fixture.command(t, "http://127.0.0.1:1", "worker")
	environment := process.Env[:0]
	for _, value := range process.Env {
		if !strings.HasPrefix(value, "GITHUB_INGRESS_TOKEN=") && !strings.HasPrefix(value, "GITLAB_INGRESS_TOKEN=") {
			environment = append(environment, value)
		}
	}
	process.Env = environment
	var output bytes.Buffer
	process.Stdout, process.Stderr = &output, &output
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	finished := false
	defer func() {
		if !finished {
			process.Process.Kill()
			process.Wait()
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		item, err := store.Factory.Get(t.Context(), datastore.Key{Kind: intake.Kind, ID: accepted.RequestID})
		if err == nil && item.Pending {
			break
		}
		if err != nil && !errors.Is(err, datastore.ErrNotFound) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not dispatch without provider credentials")
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
	if err := process.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err = process.Wait()
	finished = true
	if err != nil {
		t.Fatalf("worker shutdown: %v\n%s", err, output.String())
	}
}
