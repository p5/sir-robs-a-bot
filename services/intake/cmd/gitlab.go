package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	queue "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/providers/gitlab"
)

type providerOptions struct {
	Account        string
	AllowedUsers   string
	Since          string
	APIURL         string
	Listen         string
	QueueNamespace string
	Storage        string
}

func runGitLab(ctx context.Context, options providerOptions, args []string, output io.Writer) error {
	command := args[0]
	switch command {
	case "redrive-work":
		if len(args) != 3 {
			return errors.New("redrive-work requires a kind and ID")
		}
	case "inspect", "receipt", "redrive-ack":
		if len(args) != 2 {
			return errors.New("command requires one request ID")
		}
	case "migrate", "activate", "probe", "scan", "poll", "serve", "list", "relay", "acknowledge", "worker":
		if len(args) != 1 {
			return errors.New("unexpected command arguments")
		}
	default:
		return errors.New("unknown GitLab intake command")
	}
	emit := func(value any) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, string(raw))
		return err
	}
	var store backend
	if command != "probe" {
		var target queue.Store
		var closeStore func()
		var err error
		store, target, closeStore, err = openStorage(ctx, options.Storage, options.QueueNamespace)
		if err != nil {
			return err
		}
		defer closeStore()
		if command == "redrive-work" {
			return runRedrive(ctx, store, args[1], args[2])
		}
		if command == "worker" {
			return runDispatch(ctx, store)
		}

		operation, stop := context.WithTimeout(ctx, time.Minute)
		defer stop()
		switch command {
		case "migrate":
			if err := store.Migrate(operation); err != nil {
				return err
			}
			return nil
		case "inspect":
			request, err := store.Get(operation, args[1])
			if err != nil {
				return err
			}
			return emit(request)
		case "relay":

			count := 0
			for {
				found, err := store.DeliverOne(operation, target)
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
	client, err := gitlab.NewClient(&http.Client{Timeout: 30 * time.Second}, options.APIURL, os.Getenv("GITLAB_INGRESS_TOKEN"))
	if err != nil {
		return err
	}
	identityContext, stop := context.WithTimeout(ctx, 30*time.Second)
	account, err := client.Identity(identityContext)
	stop()
	if err != nil {
		return err
	}
	if !strings.EqualFold(account.Username, options.Account) {
		return errors.New("GitLab token belongs to a different account")
	}
	connection := client.Connection(account.ID)
	sender := gitlab.Acknowledger{Client: client, AccountID: account.ID}
	operation, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	switch command {
	case "activate":
		since, err := time.Parse(time.RFC3339, options.Since)
		if err != nil {
			return errors.New("activate requires RFC3339 -since")
		}
		return store.Activate(operation, connection, since)
	case "list":
		ids, err := store.List(operation, connection)
		if err != nil {
			return err
		}
		return emit(ids)
	case "receipt":
		status, err := store.Acknowledgement(operation, connection, args[1])
		if err != nil {
			return err
		}
		return emit(status)
	case "redrive-ack":
		return store.RedriveAcknowledgement(operation, connection, args[1])
	case "acknowledge":
		count, err := store.AcknowledgeDue(operation, sender)
		if emitErr := emit(struct{ Acknowledged int }{count}); emitErr != nil {
			return emitErr
		}
		return err
	}
	allowed, err := parseUsers(options.AllowedUsers)
	if err != nil {
		return err
	}
	var since time.Time
	if command == "probe" {
		since, err = time.Parse(time.RFC3339, options.Since)
		if err != nil || since.IsZero() || since.After(time.Now()) {
			return errors.New("probe requires past RFC3339 -since")
		}
	} else {
		since, err = store.Activation(operation, connection)
		if err != nil {
			return errors.New("load GitLab activation; run activate first")
		}
	}
	policy := gitlab.Policy{Connection: connection, Account: account, AllowedUsers: allowed, Since: since}
	if command == "serve" {
		hook, err := gitlab.NewWebhook(client, policy, store, os.Getenv("GITLAB_WEBHOOK_SIGNING_TOKEN"))
		if err != nil {
			return err
		}
		return serveWebhook(ctx, options.Listen, hook, func(ctx context.Context) error {
			_, err := store.AcknowledgeDue(ctx, sender)
			return err
		})
	}
	poller := gitlab.Poller{Client: client, Policy: policy, Intake: store}
	if command == "probe" {
		poller.Intake = intake.AcceptFunc(func(_ context.Context, submission intake.Submission) (intake.Acceptance, error) {
			id, err := submission.Source.RequestID()
			if err != nil {
				return intake.Acceptance{}, err
			}
			return intake.Acceptance{RequestID: id}, emit(struct {
				ID     string
				Source intake.Source
			}{id, submission.Source})
		})
	}
	for {
		pass, stop := context.WithTimeout(ctx, 2*time.Minute)
		sweep, scanErr := poller.Scan(pass)
		stop()
		delay := max(time.Minute, sweep.NextPoll)
		if failure, ok := errors.AsType[*gitlab.APIError](scanErr); ok {
			delay = max(delay, failure.RetryAfter)
		}
		failure, limited := errors.AsType[*gitlab.APIError](scanErr)
		if command != "probe" && (!limited || failure.Status != 429) {
			delivery, stop := context.WithTimeout(ctx, time.Minute)
			_, deliveryErr := store.AcknowledgeDue(delivery, sender)
			stop()
			scanErr = errors.Join(deliveryErr, scanErr)
			if failure, ok := errors.AsType[*intake.DeliveryError](deliveryErr); ok {
				delay = max(delay, failure.RetryAfter)
			}
		}
		if err := emit(sweep); err != nil {
			return err
		}
		if command != "poll" {
			return scanErr
		}
		if scanErr != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, "GitLab scan failed:", scanErr)
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
