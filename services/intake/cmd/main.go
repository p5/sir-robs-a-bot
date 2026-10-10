// Command intake ingests external requests through provider adapters. It does not execute requests.
package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	queue "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/providers/github"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("intake", flag.ContinueOnError)
	storage := flags.String("storage", storageDefault(), "durable storage: postgres or aws")
	provider := flags.String("provider", "github", "intake provider: github or gitlab")
	listen := flags.String("listen", "127.0.0.1:8080", "webhook listener address")
	account := flags.String("account", "sir-robs-a-bot", "expected provider account username")
	allow := flags.String("allow-user-ids", "", "comma-separated provider user IDs authorized to submit requests")
	since := flags.String("since", "", "RFC3339 activation time, required for activate and probe")
	apiURL := flags.String("api-url", "", "provider API origin; defaults to api.github.com or gitlab.com")
	namespace := flags.String("queue-namespace", "factory", "coordination queue namespace")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		return errors.New("command required: probe, migrate, activate, scan, poll, list, inspect ID, relay, acknowledge, receipt ID, redrive-ack ID, serve, worker, redrive-work KIND ID")
	}
	command := flags.Arg(0)
	if *apiURL == "" {
		*apiURL = "https://api.github.com"
		if *provider == "gitlab" {
			*apiURL = "https://gitlab.com"
		}
	}
	if *provider == "gitlab" {
		return runGitLab(ctx, providerOptions{Account: *account, AllowedUsers: *allow, Since: *since, APIURL: *apiURL, Listen: *listen, QueueNamespace: *namespace, Storage: *storage}, flags.Args(), output)
	}
	if *provider != "github" {
		return errors.New("unknown intake provider")
	}
	if command == "redrive-work" {
		if flags.NArg() != 3 {
			return errors.New("redrive-work requires a kind and ID")
		}
	} else if command == "inspect" || command == "receipt" || command == "redrive-ack" {
		if flags.NArg() != 2 {
			return errors.New("command requires one request ID")
		}
	} else if flags.NArg() != 1 {
		return errors.New("unexpected command arguments")
	}
	switch command {
	case "probe", "migrate", "activate", "scan", "poll", "list", "inspect", "relay", "acknowledge", "receipt", "redrive-ack", "serve", "worker", "redrive-work":
	default:
		return errors.New("unknown intake command")
	}
	emit := func(value any) error {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, string(encoded))
		return err
	}
	var store backend
	if command != "probe" {
		var target queue.Store
		var closeStore func()
		var err error
		store, target, closeStore, err = openStorage(ctx, *storage, *namespace)
		if err != nil {
			return err
		}
		defer closeStore()
		if command == "redrive-work" {
			return runRedrive(ctx, store, flags.Arg(1), flags.Arg(2))
		}
		if command == "worker" {
			return runDispatch(ctx, store)
		}

		if command == "migrate" {
			migration, stop := context.WithTimeout(ctx, time.Minute)
			defer stop()
			if err := store.Migrate(migration); err != nil {
				return err
			}
			return nil
		}
		if command == "inspect" {
			inspection, stop := context.WithTimeout(ctx, 15*time.Second)
			defer stop()
			request, err := store.Get(inspection, flags.Arg(1))
			if err != nil {
				return err
			}
			return emit(request)
		}
		if command == "relay" {
			delivery, stop := context.WithTimeout(ctx, time.Minute)
			defer stop()

			count := 0
			for {
				found, err := store.DeliverOne(delivery, target)
				if err != nil {
					return err
				}
				if !found {
					return emit(struct{ Delivered int }{count})
				}
				count++
			}
		}
	}
	// Authentication is explicit. Never inherit the developer's gh credentials.
	origin, err := url.Parse(*apiURL)
	if err != nil {
		return errors.New("invalid GitHub API origin")
	}
	loopback := net.ParseIP(origin.Hostname())
	if *apiURL != "https://api.github.com" && !(origin.Scheme == "http" && loopback != nil && loopback.IsLoopback()) {
		return errors.New("API origin must be api.github.com or an HTTP loopback test server")
	}
	client, err := github.NewClient(&http.Client{Timeout: 30 * time.Second}, *apiURL, os.Getenv("GITHUB_INGRESS_TOKEN"))
	if err != nil {
		return err
	}
	identityContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	identity, err := client.Identity(identityContext)
	cancel()
	if err != nil {
		return err
	}
	if !strings.EqualFold(identity.Login, *account) {
		return errors.New("GitHub token belongs to a different account")
	}
	if command == "receipt" || command == "redrive-ack" {
		inspection, stop := context.WithTimeout(ctx, 15*time.Second)
		defer stop()
		connection := github.Connection(identity.ID)
		if command == "redrive-ack" {
			return store.RedriveAcknowledgement(inspection, connection, flags.Arg(1))
		}
		status, err := store.Acknowledgement(inspection, connection, flags.Arg(1))
		if err != nil {
			return err
		}
		return emit(status)
	}
	if command == "acknowledge" {
		delivery, stop := context.WithTimeout(ctx, time.Minute)
		defer stop()
		count, err := store.AcknowledgeDue(delivery, github.Acknowledger{Client: client, AccountID: identity.ID})
		if emitErr := emit(struct{ Acknowledged int }{count}); emitErr != nil {
			return emitErr
		}
		return err
	}
	if command == "list" {
		inspection, stop := context.WithTimeout(ctx, 15*time.Second)
		defer stop()
		ids, err := store.List(inspection, github.Connection(identity.ID))
		if err != nil {
			return err
		}
		return emit(ids)
	}
	if command == "activate" {
		floor, err := time.Parse(time.RFC3339, *since)
		if err != nil {
			return errors.New("activate requires -since with an RFC3339 timestamp")
		}
		activation, stop := context.WithTimeout(ctx, 15*time.Second)
		defer stop()
		if err := store.Activate(activation, github.Connection(identity.ID), floor); err != nil {
			return err
		}
		return emit(struct {
			AccountID int64
			Since     time.Time
		}{identity.ID, floor})
	}
	allowed, err := parseUsers(*allow)
	if err != nil {
		return err
	}
	var floor time.Time
	if command == "probe" {
		floor, err = time.Parse(time.RFC3339, *since)
		if err != nil || floor.After(time.Now()) {
			return errors.New("probe requires a past -since timestamp")
		}
	} else {
		activation, stop := context.WithTimeout(ctx, 15*time.Second)
		floor, err = store.Activation(activation, github.Connection(identity.ID))
		stop()
		if err != nil {
			return fmt.Errorf("load account activation, run activate first: %w", err)
		}
	}
	poller := github.Poller{Client: client, Policy: github.Policy{Account: identity, AllowedUsers: allowed, Since: floor}, Intake: store}
	if command == "serve" {
		hook, err := github.NewWebhook(client, poller.Policy, store, os.Getenv("GITHUB_WEBHOOK_SECRET"))
		if err != nil {
			return err
		}
		return serveWebhook(ctx, *listen, hook, func(ctx context.Context) error {
			_, err := store.AcknowledgeDue(ctx, github.Acknowledger{Client: client, AccountID: identity.ID})
			return err
		})
	}
	if command == "probe" {
		poller.Intake = intake.AcceptFunc(func(_ context.Context, submission intake.Submission) (intake.Acceptance, error) {
			// Probe output contains source identities, never source bodies or credentials.
			id, err := submission.Source.RequestID()
			if err != nil {
				return intake.Acceptance{}, err
			}
			return intake.Acceptance{RequestID: id}, emit(struct {
				ID     string
				Source intake.Source
				Author string
			}{id, submission.Source, submission.Author})
		})
	}
	for {
		sweepContext, stop := context.WithTimeout(ctx, 2*time.Minute)
		sweep, err := poller.Scan(sweepContext)
		stop()
		acknowledged := 0
		if command != "probe" {
			failure, limited := errors.AsType[*github.APIError](err)
			if !limited || !failure.RateLimited {
				delivery, cancel := context.WithTimeout(ctx, time.Minute)
				count, deliveryErr := store.AcknowledgeDue(delivery, github.Acknowledger{Client: client, AccountID: identity.ID})
				cancel()
				acknowledged = count
				err = errors.Join(deliveryErr, err)
			}
		}
		summary := struct {
			Threads         int
			Acknowledged    int
			Accepted        int
			Existing        int
			FailedThreads   int
			FailedSources   int
			NextPollSeconds int64
		}{sweep.Threads, acknowledged, sweep.Accepted, sweep.Existing, sweep.FailedThreads, sweep.FailedSources, int64(sweep.NextPoll / time.Second)}
		if emitErr := emit(summary); emitErr != nil {
			return emitErr
		}
		if command != "poll" {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		delay := max(time.Minute, sweep.NextPoll)
		if err != nil {
			fmt.Fprintln(os.Stderr, "mention scan failed:", err)
			if failure, ok := errors.AsType[*github.APIError](err); ok {
				delay = max(delay, failure.RetryAfter)
			}
			if failure, ok := errors.AsType[*intake.DeliveryError](err); ok {
				delay = max(delay, failure.RetryAfter)
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func parseUsers(text string) (map[int64]bool, error) {
	users := make(map[int64]bool)
	for value := range strings.SplitSeq(text, ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || id <= 0 {
			return nil, errors.New("allow-user-ids requires positive numeric user IDs")
		}
		users[id] = true
	}
	return users, nil
}
