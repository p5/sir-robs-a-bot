package gitlab

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
)

type Policy struct {
	Connection   intake.Connection
	Account      User
	AllowedUsers map[int64]bool
	Since        time.Time
}

func (policy Policy) Validate() error {
	if policy.Connection.Validate() != nil || policy.Connection.Provider != "gitlab" || policy.Account.ID <= 0 || policy.Account.Username == "" || policy.Since.IsZero() || len(policy.AllowedUsers) == 0 {
		return errors.New("GitLab connection, account, activation and user allowlist are required")
	}
	for id, allowed := range policy.AllowedUsers {
		if id <= 0 || !allowed {
			return errors.New("invalid GitLab user allowlist")
		}
	}
	return nil
}

// Receipt contains numeric API coordinates, never a payload-supplied URL.
type Receipt struct {
	ProjectID int64
	Resource  string
	IID       int64
}

func (receipt Receipt) route(noteID int64) (string, error) {
	if receipt.ProjectID <= 0 || receipt.IID <= 0 || noteID <= 0 || (receipt.Resource != "issues" && receipt.Resource != "merge_requests") {
		return "", errors.New("invalid GitLab receipt coordinates")
	}
	return fmt.Sprintf("/projects/%d/%s/%d/notes/%d", receipt.ProjectID, receipt.Resource, receipt.IID, noteID), nil
}

type Note struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	Author    User      `json:"author"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	System    bool      `json:"system"`
}

func ParseMention(body, username string) (string, bool) {
	if len(body) > 64<<10 || !utf8.ValidString(body) || strings.ContainsRune(body, 0) || username == "" {
		return "", false
	}
	text := strings.TrimSpace(body)
	prefix := "@" + username
	if len(text) <= len(prefix) || !strings.EqualFold(text[:len(prefix)], prefix) {
		return "", false
	}
	switch text[len(prefix)] {
	case ' ', '\t', '\r', '\n':
	default:
		return "", false
	}
	instruction := strings.TrimSpace(text[len(prefix):])
	return instruction, instruction != "" && len(instruction) <= 16<<10
}

// Candidate accepts an authorized creation observation. Both transports must
// verify that the current API note remains unedited before calling it.
func (policy Policy) Candidate(receipt Receipt, note Note, observed time.Time) (intake.Submission, bool, error) {
	if err := policy.Validate(); err != nil {
		return intake.Submission{}, false, err
	}
	if note.System || note.Author.Bot || note.Author.ID == policy.Account.ID || !policy.AllowedUsers[note.Author.ID] || note.CreatedAt.IsZero() || note.CreatedAt.Before(policy.Since) {
		return intake.Submission{}, false, nil
	}
	instruction, ok := ParseMention(note.Body, policy.Account.Username)
	if !ok {
		return intake.Submission{}, false, nil
	}
	if _, err := receipt.route(note.ID); err != nil {
		return intake.Submission{}, false, err
	}
	metadata, err := json.Marshal(receipt)
	if err != nil {
		return intake.Submission{}, false, err
	}
	actor := strconv.FormatInt(note.Author.ID, 10)
	submission := intake.Submission{Source: intake.Source{Connection: policy.Connection, Scope: strconv.FormatInt(receipt.ProjectID, 10), Kind: "note", ID: strconv.FormatInt(note.ID, 10)}, Author: actor, SubmittedBy: actor, Body: note.Body, Instruction: instruction, CreatedAt: note.CreatedAt, ObservedAt: observed, Metadata: metadata, ReceiptMode: intake.ReceiptAsync}
	if err := submission.Validate(); err != nil {
		return intake.Submission{}, false, err
	}
	return submission, true, nil
}
