package factorytests

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
	"strconv"
	"strings"
	"testing"
	"time"
)

func gitlabSignature(messageID, timestamp string, raw []byte) string {
	digest := hmac.New(sha256.New, []byte(prototypeSecret))
	digest.Write([]byte(messageID + "." + timestamp + "."))
	digest.Write(raw)
	return "v1," + base64.StdEncoding.EncodeToString(digest.Sum(nil))
}

func gitlabSubmission(instance, messageID, timestamp, signature string, raw []byte, now time.Time) (intake.Submission, error) {
	seconds, timestampErr := strconv.ParseInt(timestamp, 10, 64)
	if instance == "" || messageID == "" || timestampErr != nil || seconds < now.Unix()-300 || seconds > now.Unix()+300 || !hmac.Equal([]byte(signature), []byte(gitlabSignature(messageID, timestamp, raw))) || len(raw) > 64<<10 {
		return intake.Submission{}, errors.New("invalid GitLab hook")
	}
	var event struct {
		Kind      string `json:"object_kind"`
		ProjectID int64  `json:"project_id"`
		User      struct {
			ID int64 `json:"id"`
		} `json:"user"`
		Note struct {
			ID       int64     `json:"id"`
			AuthorID int64     `json:"author_id"`
			Action   string    `json:"action"`
			Kind     string    `json:"noteable_type"`
			Text     string    `json:"note"`
			Created  time.Time `json:"created_at"`
		} `json:"object_attributes"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return intake.Submission{}, err
	}
	// Limit this prototype to create events. Edits require fresh provenance.
	if event.Kind != "note" || event.ProjectID <= 0 || event.Note.ID <= 0 || event.User.ID != 7 || event.Note.AuthorID != 7 || event.Note.Action != "create" || event.Note.Kind != "Issue" {
		return intake.Submission{}, errors.New("unsupported or unauthorized GitLab note")
	}
	instruction, ok := strings.CutPrefix(event.Note.Text, "@bot ")
	if !ok {
		return intake.Submission{}, errors.New("not a command")
	}
	return intake.Submission{ReceiptMode: intake.ReceiptAsync, Source: intake.Source{Connection: intake.Connection{Provider: "gitlab", Account: instance + "/bot-42"}, Scope: strconv.FormatInt(event.ProjectID, 10), Kind: "note", ID: strconv.FormatInt(event.Note.ID, 10)}, Author: "7", SubmittedBy: "7", Body: event.Note.Text, Instruction: instruction, CreatedAt: event.Note.Created, ObservedAt: now, Metadata: jsontext.Value(`{"issueIID":3}`)}, nil
}

func TestPrototypeGitLabSelfHostedIsolation(t *testing.T) {
	fixture := open(t)
	raw := []byte(`{"object_kind":"note","project_id":11,"user":{"id":7},"object_attributes":{"id":1,"author_id":7,"action":"create","noteable_type":"Issue","note":"@bot investigate","created_at":"2026-10-10T10:00:00Z"}}`)
	now := time.Now().UTC()
	timestamp := strconv.FormatInt(now.Unix(), 10)
	previous := ""
	for _, instance := range []string{"instance-one", "instance-two"} {
		submission, err := gitlabSubmission(instance, "message-one", timestamp, gitlabSignature("message-one", timestamp, raw), raw, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.Store.Activate(t.Context(), submission.Source.Connection, fixture.Since); err != nil {
			t.Fatal(err)
		}
		accepted, err := fixture.Store.Accept(t.Context(), submission)
		if err != nil || !accepted.Created || accepted.RequestID == previous {
			t.Fatalf("GitLab isolation: %+v %v", accepted, err)
		}
		replay, err := fixture.Store.Accept(t.Context(), submission)
		if err != nil || replay.Created || replay.RequestID != accepted.RequestID {
			t.Fatalf("GitLab replay: %+v %v", replay, err)
		}
		previous = accepted.RequestID
	}
	if _, err := gitlabSubmission("instance-one", "message-one", timestamp, "invalid", raw, now); err == nil {
		t.Fatal("unsigned GitLab hook accepted")
	}
	update := []byte(strings.Replace(string(raw), `"action":"create"`, `"action":"update"`, 1))
	if _, err := gitlabSubmission("instance-one", "message-two", timestamp, gitlabSignature("message-two", timestamp, update), update, now); err == nil {
		t.Fatal("edit accepted without provenance")
	}
}
