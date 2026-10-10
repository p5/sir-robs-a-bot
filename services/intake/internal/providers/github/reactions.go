package github

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
	"net/http"
	"strings"
)

// ReactEyes acknowledges receipt, not execution. GitHub returns the existing
// reaction when the same account repeats this operation after a lost reply.
func (client *Client) ReactEyes(ctx context.Context, repository, sourceType string, sourceID, issueNumber, accountID int64) error {
	endpoint, err := client.reactionEndpoint(repository, sourceType, sourceID, issueNumber)
	if err != nil || accountID <= 0 {
		return errors.New("invalid GitHub reaction identity")
	}
	body, _, err := client.send(ctx, http.MethodPost, endpoint, strings.NewReader(`{"content":"eyes"}`), http.StatusCreated)
	if err != nil {
		if _, ok := errors.AsType[*APIError](err); ok {
			return err
		}
		return &intake.DeliveryError{Err: err, Disposition: intake.DeliveryUncertain}
	}
	var reaction struct {
		ID      int64  `json:"id"`
		Content string `json:"content"`
		User    User   `json:"user"`
	}
	if err := json.Unmarshal(body, &reaction); err != nil || reaction.ID <= 0 || reaction.Content != "eyes" || reaction.User.ID != accountID {
		return &intake.DeliveryError{Err: errors.New("invalid GitHub acknowledgement response"), Disposition: intake.DeliveryUncertain}
	}
	return nil
}

func (client *Client) reactionEndpoint(repository, sourceType string, sourceID, issueNumber int64) (string, error) {
	if !validRepository(repository) || sourceID <= 0 || issueNumber <= 0 {
		return "", errors.New("invalid GitHub reaction source")
	}
	var resource string
	switch sourceType {
	case "comment":
		resource = fmt.Sprintf("/issues/comments/%d/reactions", sourceID)
	case "issue":
		// Pull request bodies use the issue reactions endpoint too.
		resource = fmt.Sprintf("/issues/%d/reactions", issueNumber)
	default:
		return "", errors.New("unsupported GitHub reaction source")
	}
	endpoint := client.base.String() + "/repos/" + repository + resource
	if _, err := client.safeURL(endpoint); err != nil {
		return "", err
	}
	return endpoint, nil
}
