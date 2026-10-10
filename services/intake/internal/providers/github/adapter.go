package github

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"strconv"

	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
)

func Connection(accountID int64) intake.Connection {
	return intake.Connection{Provider: "github", Account: strconv.FormatInt(accountID, 10)}
}

// Receipt is provider-owned metadata. Intake persists it without interpreting it.
type Receipt struct {
	Repository  string
	IssueNumber int64
}

// Acknowledger binds the authenticated account to GitHub's reaction operation.
type Acknowledger struct {
	Client    *Client
	AccountID int64
}

func (sender Acknowledger) Connection() intake.Connection { return Connection(sender.AccountID) }

func (sender Acknowledger) Acknowledge(ctx context.Context, request intake.Request) error {
	if sender.Client == nil || sender.AccountID <= 0 || request.Source.Connection != sender.Connection() {
		return errors.New("GitHub acknowledgement connection mismatch")
	}
	var receipt Receipt
	if err := json.Unmarshal(request.Metadata, &receipt); err != nil {
		return errors.New("invalid GitHub receipt metadata")
	}
	sourceID, err := strconv.ParseInt(request.Source.ID, 10, 64)
	if err != nil {
		return errors.New("invalid GitHub receipt source")
	}
	err = sender.Client.ReactEyes(ctx, receipt.Repository, request.Source.Kind, sourceID, receipt.IssueNumber, sender.AccountID)
	if failure, ok := errors.AsType[*APIError](err); ok {
		scope := intake.RetryRequest
		if failure.RateLimited {
			scope = intake.RetryConnection
		}
		disposition := intake.DeliveryTerminal
		if failure.RateLimited {
			disposition = intake.DeliveryRetry
		} else if failure.Status >= 500 {
			disposition = intake.DeliveryUncertain
		}
		return &intake.DeliveryError{Err: err, RetryAfter: failure.RetryAfter, RetryScope: scope, Disposition: disposition}
	}
	return err
}

var _ intake.Acknowledger = Acknowledger{}

// ObserveAcknowledgement performs only reads. Absence cannot prove that an
// interrupted POST will never complete, so intake still stops uncertain work.
func (sender Acknowledger) ObserveAcknowledgement(ctx context.Context, request intake.Request) (bool, error) {
	if sender.Client == nil || sender.AccountID <= 0 || request.Source.Connection != sender.Connection() {
		return false, errors.New("GitHub acknowledgement connection mismatch")
	}
	var receipt Receipt
	if json.Unmarshal(request.Metadata, &receipt) != nil {
		return false, errors.New("invalid GitHub receipt metadata")
	}
	id, err := strconv.ParseInt(request.Source.ID, 10, 64)
	if err != nil {
		return false, err
	}
	route, err := sender.Client.reactionEndpoint(receipt.Repository, request.Source.Kind, id, receipt.IssueNumber)
	if err != nil {
		return false, err
	}
	found := errors.New("eyes found")
	err = sender.Client.pages(ctx, route, func(raw []byte, _ http.Header) error {
		var reactions []struct {
			ID      int64  `json:"id"`
			Content string `json:"content"`
			User    User   `json:"user"`
		}
		if json.Unmarshal(raw, &reactions) != nil {
			return errors.New("invalid GitHub reaction listing")
		}
		for _, reaction := range reactions {
			if reaction.ID > 0 && reaction.Content == "eyes" && reaction.User.ID == sender.AccountID {
				return found
			}
		}
		return nil
	})
	if errors.Is(err, found) {
		return true, nil
	}
	if failure, ok := errors.AsType[*APIError](err); ok && failure.RateLimited {
		return false, &intake.DeliveryError{Err: err, RetryScope: intake.RetryConnection, RetryAfter: failure.RetryAfter}
	}
	return false, err
}
