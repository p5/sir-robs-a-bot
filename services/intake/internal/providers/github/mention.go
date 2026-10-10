// GitHub policy owns command syntax and submission authorization.
package github

import (
	"encoding/json/v2"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
)

// Policy grants request submission only. It grants no execution or publication.
type Policy struct {
	Account      User
	AllowedUsers map[int64]bool
	Since        time.Time
}

func (policy Policy) Validate() error {
	if policy.Account.ID <= 0 || policy.Account.Login == "" || policy.Since.IsZero() || len(policy.AllowedUsers) == 0 {
		return errors.New("account, activation time, and a nonempty user allowlist are required")
	}
	for id, allowed := range policy.AllowedUsers {
		if id <= 0 || !allowed {
			return errors.New("allowlist must contain positive GitHub user IDs")
		}
	}
	return nil
}

// Candidate validates the original author and syntax. The poller must also
// authorize the current editor before saving this provisional request.
func (policy Policy) Candidate(repository Repository, sourceType string, source Source) (intake.Submission, bool, error) {
	if err := policy.Validate(); err != nil {
		return intake.Submission{}, false, err
	}
	if source.User.Type != "User" || source.User.ID == policy.Account.ID || !policy.AllowedUsers[source.User.ID] {
		return intake.Submission{}, false, nil
	}
	if source.CreatedAt.IsZero() || source.CreatedAt.Before(policy.Since) {
		return intake.Submission{}, false, nil
	}
	instruction, ok := ParseMention(source.Body, policy.Account.Login)
	if !ok {
		return intake.Submission{}, false, nil
	}
	if source.ID <= 0 || source.Number <= 0 || repository.ID <= 0 || repository.FullName == "" || (sourceType != "comment" && sourceType != "issue") {
		return intake.Submission{}, false, errors.New("mention has an invalid source identity")
	}
	metadata, err := json.Marshal(Receipt{Repository: repository.FullName, IssueNumber: source.Number})
	if err != nil {
		return intake.Submission{}, false, err
	}
	request := intake.Submission{ReceiptMode: intake.ReceiptAsync,
		Source: intake.Source{Connection: Connection(policy.Account.ID), Scope: strconv.FormatInt(repository.ID, 10), Kind: sourceType, ID: strconv.FormatInt(source.ID, 10)},
		Author: strconv.FormatInt(source.User.ID, 10),
		Body:   source.Body, Instruction: instruction, CreatedAt: source.CreatedAt, ObservedAt: time.Now().UTC(), Metadata: metadata,
	}
	return request, true, nil
}

// ParseMention accepts only a leading command addressed to this account. Quoted
// text, fenced code, inline mentions, other handles, and empty commands do not
// submit work. This is command syntax, not a complete Markdown mention parser.
func ParseMention(body, login string) (string, bool) {
	if len(body) > 64<<10 || !utf8.ValidString(body) || strings.ContainsRune(body, 0) || login == "" {
		return "", false
	}
	text := strings.TrimSpace(body)
	prefix := "@" + login
	if len(text) <= len(prefix) || !strings.EqualFold(text[:len(prefix)], prefix) {
		return "", false
	}
	switch text[len(prefix)] {
	case ' ', '\t', '\r', '\n':
	default:
		return "", false
	}
	instruction := strings.TrimSpace(text[len(prefix):])
	if instruction == "" || len(instruction) > 16<<10 {
		return "", false
	}
	return instruction, true
}
