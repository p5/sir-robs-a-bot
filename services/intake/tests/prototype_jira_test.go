package factorytests

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
	"strings"
	"testing"
	"time"
)

// Jira prototypes consume an authenticated REST observation, not an unsigned
// webhook. Only a narrow, explicit ADF subset is supported here.
type prototypeADF struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Attrs struct {
		ID string `json:"id"`
	} `json:"attrs"`
	Content []prototypeADF `json:"content"`
}

func prototypeADFText(node prototypeADF, depth int) (string, error) {
	if depth > 8 {
		return "", errors.New("ADF nesting exceeds prototype limit")
	}
	switch node.Type {
	case "text":
		return node.Text, nil
	case "mention":
		if node.Attrs.ID != "jira-bot" {
			return "", errors.New("unexpected mention")
		}
		return "@bot", nil
	case "doc", "paragraph":
		var text strings.Builder
		for _, child := range node.Content {
			part, err := prototypeADFText(child, depth+1)
			if err != nil {
				return "", err
			}
			text.WriteString(part)
		}
		if node.Type == "paragraph" {
			text.WriteByte('\n')
		}
		return text.String(), nil
	default:
		return "", errors.New("unsupported ADF node")
	}
}
func jiraSubmission(raw []byte, now time.Time) (intake.Submission, error) {
	if len(raw) > 64<<10 {
		return intake.Submission{}, errors.New("comment too large")
	}
	var comment struct {
		ID     string `json:"id"`
		Author struct {
			ID string `json:"accountId"`
		} `json:"author"`
		Editor struct {
			ID string `json:"accountId"`
		} `json:"updateAuthor"`
		Created time.Time    `json:"created"`
		Body    prototypeADF `json:"body"`
	}
	if err := json.Unmarshal(raw, &comment); err != nil {
		return intake.Submission{}, err
	}
	if comment.Author.ID != "jira-user" || comment.Editor.ID != "jira-user" {
		return intake.Submission{}, errors.New("Jira actor not allowlisted")
	}
	text, err := prototypeADFText(comment.Body, 0)
	if err != nil {
		return intake.Submission{}, err
	}
	instruction, ok := strings.CutPrefix(text, "@bot ")
	if !ok {
		return intake.Submission{}, errors.New("not a command")
	}
	return intake.Submission{ReceiptMode: intake.ReceiptAsync, Source: intake.Source{Connection: intake.Connection{Provider: "jira", Account: "cloud-one/installation-one"}, Scope: "10001", Kind: "comment", ID: comment.ID}, Author: comment.Author.ID, SubmittedBy: comment.Editor.ID, Body: text, Instruction: strings.TrimSpace(instruction), CreatedAt: comment.Created, ObservedAt: now, Metadata: jsontext.Value(`{"issueId":"10001","commentId":"20001"}`)}, nil
}

func TestPrototypeJiraPollingStructuredComment(t *testing.T) {
	fixture := open(t)
	raw := []byte(`{"id":"20001","author":{"accountId":"jira-user"},"updateAuthor":{"accountId":"jira-user"},"created":"2026-10-10T10:00:00Z","body":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"mention","attrs":{"id":"jira-bot"}},{"type":"text","text":" investigate"}]}]}}`)
	submission, err := jiraSubmission(raw, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.Store.Activate(t.Context(), submission.Source.Connection, fixture.Since); err != nil {
		t.Fatal(err)
	}
	accepted, err := fixture.Store.Accept(t.Context(), submission)
	if err != nil || !accepted.Created {
		t.Fatalf("Jira accept: %+v %v", accepted, err)
	}
	saved, err := fixture.Store.Get(t.Context(), accepted.RequestID)
	if err != nil || saved.Instruction != "investigate" {
		t.Fatalf("Jira snapshot: %+v %v", saved, err)
	}
	badEditor := []byte(strings.Replace(string(raw), `"updateAuthor":{"accountId":"jira-user"}`, `"updateAuthor":{"accountId":"untrusted"}`, 1))
	if _, err := jiraSubmission(badEditor, time.Now()); err == nil {
		t.Fatal("untrusted editor accepted")
	}
	unsupported := []byte(strings.Replace(string(raw), `"type":"paragraph"`, `"type":"codeBlock"`, 1))
	if _, err := jiraSubmission(unsupported, time.Now()); err == nil {
		t.Fatal("code block accepted as instruction")
	}
}
