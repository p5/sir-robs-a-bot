package gitlab

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
)

type Acknowledger struct {
	Client    *Client
	AccountID int64
}

func (sender Acknowledger) Connection() intake.Connection {
	return sender.Client.Connection(sender.AccountID)
}

func (sender Acknowledger) Acknowledge(ctx context.Context, request intake.Request) error {
	if sender.Client == nil || sender.AccountID <= 0 {
		return errors.New("GitLab sender is not configured")
	}
	if request.Source.Connection != sender.Connection() || request.Source.Kind != "note" {
		return errors.New("GitLab acknowledgement connection mismatch")
	}
	var receipt Receipt
	if json.Unmarshal(request.Metadata, &receipt) != nil {
		return errors.New("invalid GitLab receipt metadata")
	}
	id, err := strconv.ParseInt(request.Source.ID, 10, 64)
	if err != nil || request.Source.Scope != strconv.FormatInt(receipt.ProjectID, 10) {
		return errors.New("invalid GitLab receipt source")
	}
	route, err := receipt.route(id)
	if err != nil {
		return err
	}
	err = sender.Client.reactEyes(ctx, route+"/award_emoji", sender.AccountID)
	if _, ok := errors.AsType[*intake.DeliveryError](err); ok {
		return err
	}
	if failure, ok := errors.AsType[*APIError](err); ok {
		scope := intake.RetryRequest
		if failure.Status == 429 {
			scope = intake.RetryConnection
		}
		return &intake.DeliveryError{Err: err, RetryAfter: failure.RetryAfter, RetryScope: scope}
	}
	return err
}

type reaction struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	User User   `json:"user"`
}

// GitLab validates repeated awards by the same user. Read before posting and
// verify an existing award after a duplicate response. Never use a toggle API.
func (client *Client) reactEyes(ctx context.Context, route string, accountID int64) error {
	exists, err := client.hasEyes(ctx, route, accountID)
	if err != nil || exists {
		return err
	}
	raw, _, err := client.send(ctx, http.MethodPost, route, strings.NewReader("name=eyes"))
	if err != nil {
		if failure, ok := errors.AsType[*APIError](err); ok && (failure.Status == 400 || failure.Status == 404 || failure.Status == 409 || failure.Status == 422) {
			exists, lookupErr := client.hasEyes(ctx, route, accountID)
			if lookupErr != nil {
				return lookupErr
			}
			if exists {
				return nil
			}
		}
		// Without a confirmed response, a POST may still have committed. Stop
		// automatic retries if lookup cannot prove the reaction already exists.
		if failure, ok := errors.AsType[*APIError](err); !ok || failure.Status >= 500 {
			exists, lookupErr := client.hasEyes(ctx, route, accountID)
			if lookupErr == nil && exists {
				return nil
			}
			failure := &intake.DeliveryError{Err: err, Disposition: intake.DeliveryUncertain}
			if rateLimit, ok := errors.AsType[*APIError](lookupErr); ok && rateLimit.Status == 429 {
				failure.RetryScope = intake.RetryConnection
				failure.RetryAfter = rateLimit.RetryAfter
			}
			return failure
		}
		if failure, ok := errors.AsType[*APIError](err); ok {
			if failure.Status == 429 {
				return err
			}
			if failure.Status < 500 {
				return &intake.DeliveryError{Err: err, Disposition: intake.DeliveryTerminal}
			}
		}
		return &intake.DeliveryError{Err: err, Disposition: intake.DeliveryUncertain}
	}
	var award reaction
	if json.Unmarshal(raw, &award) != nil || award.ID <= 0 || award.Name != "eyes" || award.User.ID != accountID {
		return &intake.DeliveryError{Err: errors.New("invalid GitLab eyes reaction response"), Disposition: intake.DeliveryUncertain}
	}
	return nil
}

var errEyesFound = errors.New("GitLab eyes reaction found")

func (client *Client) hasEyes(ctx context.Context, route string, accountID int64) (bool, error) {
	err := client.pages(ctx, route, func(raw []byte) error {
		var awards []reaction
		if json.Unmarshal(raw, &awards) != nil {
			return errors.New("invalid GitLab reaction listing")
		}
		for _, award := range awards {
			if award.ID > 0 && award.Name == "eyes" && award.User.ID == accountID {
				return errEyesFound
			}
		}
		return nil
	})
	if errors.Is(err, errEyesFound) {
		return true, nil
	}
	return false, err
}

var _ intake.Acknowledger = Acknowledger{}

func (sender Acknowledger) ObserveAcknowledgement(ctx context.Context, request intake.Request) (bool, error) {
	if sender.Client == nil || sender.AccountID <= 0 || request.Source.Connection != sender.Connection() || request.Source.Kind != "note" {
		return false, errors.New("GitLab acknowledgement connection mismatch")
	}
	var receipt Receipt
	if json.Unmarshal(request.Metadata, &receipt) != nil {
		return false, errors.New("invalid GitLab receipt metadata")
	}
	id, err := strconv.ParseInt(request.Source.ID, 10, 64)
	if err != nil || request.Source.Scope != strconv.FormatInt(receipt.ProjectID, 10) {
		return false, errors.New("invalid GitLab receipt source")
	}
	route, err := receipt.route(id)
	if err != nil {
		return false, err
	}
	observed, err := sender.Client.hasEyes(ctx, route+"/award_emoji", sender.AccountID)
	if failure, ok := errors.AsType[*APIError](err); ok && failure.Status == 429 {
		return false, &intake.DeliveryError{Err: err, RetryScope: intake.RetryConnection, RetryAfter: failure.RetryAfter}
	}
	return observed, err
}
