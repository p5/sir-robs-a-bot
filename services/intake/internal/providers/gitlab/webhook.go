package gitlab

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
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

// Webhook requires Standard Webhooks signatures. It never falls back to the
// legacy secret-token header. The configured policy owns instance identity.
type Webhook struct {
	Client *Client
	Policy Policy
	Intake intake.Acceptor
	key    []byte
}

func NewWebhook(client *Client, policy Policy, acceptor intake.Acceptor, signingToken string) (*Webhook, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(signingToken, "whsec_"))
	if err != nil || len(key) != 32 || !strings.HasPrefix(signingToken, "whsec_") {
		return nil, errors.New("GitLab signing token must encode 32 bytes with whsec_ prefix")
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if client == nil || client.Connection(policy.Account.ID) != policy.Connection || acceptor == nil {
		return nil, errors.New("GitLab intake acceptor is required")
	}
	return &Webhook{Client: client, Policy: policy, Intake: acceptor, key: key}, nil
}

func (hook *Webhook) Verify(header http.Header, raw []byte, now time.Time) bool {
	id, timestamp := header.Get("webhook-id"), header.Get("webhook-timestamp")
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if len(raw) > maxWebhookBytes || id == "" || len(id) > 512 || strings.ContainsAny(id, ".\r\n") || err != nil || seconds < now.Unix()-300 || seconds > now.Unix()+300 {
		return false
	}
	digest := hmac.New(sha256.New, hook.key)
	digest.Write([]byte(id + "." + timestamp + "."))
	digest.Write(raw)
	for signature := range strings.FieldsSeq(header.Get("webhook-signature")) {
		encoded, ok := strings.CutPrefix(signature, "v1,")
		if !ok {
			continue
		}
		candidate, err := base64.StdEncoding.DecodeString(encoded)
		if err == nil && hmac.Equal(candidate, digest.Sum(nil)) {
			return true
		}
	}
	return false
}

func (hook *Webhook) Receive(ctx context.Context, raw []byte) (intake.Acceptance, bool, error) {
	var event struct {
		Kind      string `json:"object_kind"`
		ProjectID int64  `json:"project_id"`
		User      User   `json:"user"`
		Note      struct {
			ID         int64     `json:"id"`
			AuthorID   int64     `json:"author_id"`
			ProjectID  int64     `json:"project_id"`
			NoteableID int64     `json:"noteable_id"`
			Kind       string    `json:"noteable_type"`
			Action     string    `json:"action"`
			Body       string    `json:"note"`
			CreatedAt  time.Time `json:"created_at"`
			System     bool      `json:"system"`
		} `json:"object_attributes"`
		Issue struct {
			ID  int64 `json:"id"`
			IID int64 `json:"iid"`
		} `json:"issue"`
		MergeRequest struct {
			ID  int64 `json:"id"`
			IID int64 `json:"iid"`
		} `json:"merge_request"`
	}
	if len(raw) > maxWebhookBytes || json.Unmarshal(raw, &event) != nil {
		return intake.Acceptance{}, false, errors.New("invalid GitLab webhook JSON")
	}
	if event.Kind != "note" || event.Note.Action != "create" {
		return intake.Acceptance{}, false, nil
	}
	if event.User.ID != event.Note.AuthorID || event.ProjectID <= 0 || event.Note.ProjectID != event.ProjectID {
		return intake.Acceptance{}, false, errors.New("GitLab note provenance mismatch")
	}
	if event.Note.System || event.User.Bot || event.User.ID == hook.Policy.Account.ID || !hook.Policy.AllowedUsers[event.User.ID] {
		return intake.Acceptance{}, false, nil
	}
	if _, ok := ParseMention(event.Note.Body, hook.Policy.Account.Username); !ok {
		return intake.Acceptance{}, false, nil
	}
	receipt := Receipt{ProjectID: event.ProjectID}
	switch event.Note.Kind {
	case "Issue":
		if event.Note.NoteableID != event.Issue.ID || event.Issue.ID <= 0 {
			return intake.Acceptance{}, false, errors.New("GitLab issue identity mismatch")
		}
		receipt.Resource = "issues"
		receipt.IID = event.Issue.IID
	case "MergeRequest":
		if event.Note.NoteableID != event.MergeRequest.ID || event.MergeRequest.ID <= 0 {
			return intake.Acceptance{}, false, errors.New("GitLab merge request identity mismatch")
		}
		receipt.Resource = "merge_requests"
		receipt.IID = event.MergeRequest.IID
	default:
		return intake.Acceptance{}, false, nil
	}
	// Hook templates are editable by project maintainers. A signature alone
	// cannot establish the claimed actor or text. Verify the current API note.
	route, err := receipt.route(event.Note.ID)
	if err != nil {
		return intake.Acceptance{}, false, err
	}
	response, _, err := hook.Client.send(ctx, http.MethodGet, route, nil)
	if err != nil {
		return intake.Acceptance{}, false, err
	}
	var note Note
	if json.Unmarshal(response, &note) != nil || note.ID != event.Note.ID || note.Author.ID != event.User.ID || note.Body != event.Note.Body || !note.CreatedAt.Equal(event.Note.CreatedAt) || note.UpdatedAt.IsZero() || !note.UpdatedAt.Equal(note.CreatedAt) {
		return intake.Acceptance{}, false, errors.New("GitLab note provenance is unavailable or changed")
	}
	submission, ok, err := hook.Policy.Candidate(receipt, note, time.Now().UTC())
	if err != nil || !ok {
		return intake.Acceptance{}, false, err
	}
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
	if !hook.Verify(r.Header, raw, time.Now()) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	_, _, err = hook.Receive(ctx, raw)
	if err != nil {
		http.Error(w, "GitLab event not accepted", http.StatusServiceUnavailable)
		return
	}
	// A successful response follows durable acceptance. Receipt delivery is separate.
	w.WriteHeader(http.StatusOK)
}
