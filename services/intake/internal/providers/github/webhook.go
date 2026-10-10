package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
)

const maxWebhookBytes = 2 << 20

// Webhook verifies signed creation events and then checks current provenance.
// Polling and webhooks share source IDs and provider authorization policy.
type Webhook struct {
	Client *Client
	Policy Policy
	Intake intake.Acceptor
	secret []byte
}

func NewWebhook(client *Client, policy Policy, acceptor intake.Acceptor, secret string) (*Webhook, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if client == nil || acceptor == nil || len(secret) < 32 {
		return nil, errors.New("GitHub webhook requires client, acceptor and a secret of at least 32 bytes")
	}
	return &Webhook{Client: client, Policy: policy, Intake: acceptor, secret: []byte(secret)}, nil
}

func (hook *Webhook) Verify(header http.Header, raw []byte) bool {
	encoded, ok := strings.CutPrefix(header.Get("X-Hub-Signature-256"), "sha256=")
	if !ok || len(raw) > maxWebhookBytes {
		return false
	}
	signature, err := hex.DecodeString(encoded)
	if err != nil {
		return false
	}
	digest := hmac.New(sha256.New, hook.secret)
	digest.Write(raw)
	return hmac.Equal(signature, digest.Sum(nil))
}

func (hook *Webhook) Receive(ctx context.Context, eventType string, raw []byte) (intake.Acceptance, bool, error) {
	var event struct {
		Action      string     `json:"action"`
		Repository  Repository `json:"repository"`
		Sender      User       `json:"sender"`
		Issue       Source     `json:"issue"`
		Comment     Source     `json:"comment"`
		PullRequest Source     `json:"pull_request"`
	}
	if len(raw) > maxWebhookBytes || json.Unmarshal(raw, &event) != nil {
		return intake.Acceptance{}, false, errors.New("invalid GitHub webhook JSON")
	}
	var source Source
	var kind string
	switch {
	case eventType == "issue_comment" && event.Action == "created":
		source = event.Comment
		source.Number = event.Issue.Number
		kind = "comment"
	case eventType == "issues" && event.Action == "opened":
		source = event.Issue
		kind = "issue"
	case eventType == "pull_request" && event.Action == "opened":
		source = event.PullRequest
		kind = "issue"
	default:
		return intake.Acceptance{}, false, nil
	}
	if event.Sender.ID != source.User.ID || event.Sender.Type != "User" {
		return intake.Acceptance{}, false, nil
	}
	submission, ok, err := hook.Policy.Candidate(event.Repository, kind, source)
	if err != nil || !ok {
		return intake.Acceptance{}, false, err
	}
	submitter, err := hook.Client.Submitter(ctx, kind, source)
	if err != nil {
		return intake.Acceptance{}, false, err
	}
	if submitter == hook.Policy.Account.ID || !hook.Policy.AllowedUsers[submitter] {
		return intake.Acceptance{}, false, nil
	}
	submission.SubmittedBy = strconv.FormatInt(submitter, 10)
	accepted, err := hook.Intake.Accept(ctx, submission)
	return accepted, true, err
}

func (hook *Webhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBytes))
	if err != nil {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	if !hook.Verify(r.Header, raw) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	_, _, err = hook.Receive(ctx, r.Header.Get("X-GitHub-Event"), raw)
	if err != nil {
		http.Error(w, "GitHub event not accepted", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}
