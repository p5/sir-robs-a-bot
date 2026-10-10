package factorytests

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func slackSignature(timestamp string, body []byte) string {
	digest := hmac.New(sha256.New, []byte(prototypeSecret))
	digest.Write([]byte("v0:" + timestamp + ":"))
	digest.Write(body)
	return "v0=" + hex.EncodeToString(digest.Sum(nil))
}

func slackSubmission(body []byte, timestamp, signature string, now time.Time) (intake.Submission, error) {
	if len(body) > 64<<10 {
		return intake.Submission{}, errors.New("event too large")
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || seconds < now.Unix()-300 || seconds > now.Unix()+300 || !hmac.Equal([]byte(signature), []byte(slackSignature(timestamp, body))) {
		return intake.Submission{}, errors.New("invalid Slack signature or timestamp")
	}
	var event struct {
		Type    string `json:"type"`
		Team    string `json:"team_id"`
		EventID string `json:"event_id"`
		Event   struct {
			Type    string `json:"type"`
			User    string `json:"user"`
			Text    string `json:"text"`
			Channel string `json:"channel"`
			TS      string `json:"ts"`
		} `json:"event"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		return intake.Submission{}, err
	}
	if event.Type != "event_callback" || event.Team != "T1" || event.Event.Type != "app_mention" || event.Event.User != "U7" || event.Event.Channel == "" || event.Event.TS == "" {
		return intake.Submission{}, errors.New("unsupported or unauthorized Slack event")
	}
	instruction, ok := strings.CutPrefix(event.Event.Text, "<@UBOT> ")
	if !ok || strings.TrimSpace(instruction) == "" {
		return intake.Submission{}, errors.New("not a command")
	}
	// Keep Slack message timestamps as strings. Float parsing loses identity.
	secondsText, fractional, ok := strings.Cut(event.Event.TS, ".")
	seconds, err = strconv.ParseInt(secondsText, 10, 64)
	micros, microErr := strconv.ParseInt(fractional, 10, 64)
	if !ok || err != nil || microErr != nil || len(fractional) != 6 || micros < 0 || micros >= 1000000 {
		return intake.Submission{}, errors.New("invalid message timestamp")
	}
	metadata, err := json.Marshal(struct {
		Channel   string
		Timestamp string
	}{event.Event.Channel, event.Event.TS})
	if err != nil {
		return intake.Submission{}, err
	}
	return intake.Submission{ReceiptMode: intake.ReceiptAsync, Source: intake.Source{Connection: intake.Connection{Provider: "slack", Account: "T1/UBOT"}, Scope: event.Event.Channel, Kind: "message", ID: event.Event.TS}, Author: event.Event.User, SubmittedBy: event.Event.User, Body: event.Event.Text, Instruction: instruction, CreatedAt: time.Unix(seconds, micros*1000), ObservedAt: now, Metadata: metadata}, nil
}

// prototypeSlackReceipt models the documented ok/error response contract,
// including already_reacted after a committed reaction's response was lost.
type prototypeSlackReceipt struct {
	client *http.Client
	origin string
}

func (sender prototypeSlackReceipt) Connection() intake.Connection {
	return intake.Connection{Provider: "slack", Account: "T1/UBOT"}
}
func (sender prototypeSlackReceipt) Acknowledge(ctx context.Context, accepted intake.Request) error {
	if accepted.Source.Connection != sender.Connection() {
		return errors.New("wrong Slack connection")
	}
	var metadata struct {
		Channel   string
		Timestamp string
	}
	if err := json.Unmarshal(accepted.Metadata, &metadata); err != nil {
		return err
	}
	body, err := json.Marshal(struct {
		Channel   string `json:"channel"`
		Timestamp string `json:"timestamp"`
		Name      string `json:"name"`
	}{metadata.Channel, metadata.Timestamp, "eyes"})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, "POST", sender.origin+"/reactions.add", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := sender.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return errors.New("fixture reaction failed")
	}
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.UnmarshalRead(http.MaxBytesReader(nil, response.Body, 4096), &result); err != nil {
		return err
	}
	if result.OK || result.Error == "already_reacted" {
		return nil
	}
	return errors.New("Slack rejected reaction")
}

func TestPrototypeSlackSignedWebhookAndReactionRecovery(t *testing.T) {
	fixture := open(t)
	now := time.Now().UTC()
	timestamp := strconv.FormatInt(now.Unix(), 10)
	raw := []byte(fmt.Sprintf(`{"type":"event_callback","team_id":"T1","event_id":"Ev1","event":{"type":"app_mention","user":"U7","text":"<@UBOT> investigate","channel":"C1","ts":"%d.000123"}}`, now.Unix()))
	submission, err := slackSubmission(raw, timestamp, slackSignature(timestamp, raw), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.Store.Activate(t.Context(), submission.Source.Connection, fixture.Since); err != nil {
		t.Fatal(err)
	}
	accepted, err := fixture.Store.Accept(t.Context(), submission)
	if err != nil || !accepted.Created {
		t.Fatalf("Slack acceptance: %+v %v", accepted, err)
	}
	// Retry delivery IDs do not define a new logical message.
	redelivery := []byte(strings.Replace(string(raw), "Ev1", "Ev2", 1))
	duplicate, err := slackSubmission(redelivery, timestamp, slackSignature(timestamp, redelivery), now)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := fixture.Store.Accept(t.Context(), duplicate)
	if err != nil || replay.Created || replay.RequestID != accepted.RequestID {
		t.Fatalf("Slack replay: %+v %v", replay, err)
	}
	for _, bad := range []struct {
		body                 []byte
		timestamp, signature string
	}{
		{append(append([]byte(nil), raw...), ' '), timestamp, slackSignature(timestamp, raw)},
		{raw, strconv.FormatInt(now.Add(-6*time.Minute).Unix(), 10), slackSignature(strconv.FormatInt(now.Add(-6*time.Minute).Unix(), 10), raw)},
		{[]byte(strings.Replace(string(raw), "U7", "U99", 1)), timestamp, "bad"},
	} {
		if _, err := slackSubmission(bad.body, bad.timestamp, bad.signature, now); err == nil {
			t.Fatal("unauthorized Slack event accepted")
		}
	}
	unauthorized := []byte(strings.Replace(string(raw), "U7", "U99", 1))
	if _, err := slackSubmission(unauthorized, timestamp, slackSignature(timestamp, unauthorized), now); err == nil {
		t.Fatal("signed unauthorized actor accepted")
	}
	var reactions, calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/reactions.add" {
			t.Error("wrong receipt target")
			w.WriteHeader(400)
			return
		}
		var payload struct {
			Channel   string `json:"channel"`
			Timestamp string `json:"timestamp"`
			Name      string `json:"name"`
		}
		if err := json.UnmarshalRead(r.Body, &payload); err != nil || payload.Channel != "C1" || payload.Timestamp != submission.Source.ID || payload.Name != "eyes" {
			t.Errorf("receipt: %+v %v", payload, err)
		}
		calls.Add(1)
		if reactions.CompareAndSwap(0, 1) {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			connection.Close()
			return
		}
		fmt.Fprint(w, `{"ok":false,"error":"already_reacted"}`)
	}))
	defer server.Close()
	sender := prototypeSlackReceipt{client: server.Client(), origin: server.URL}
	if _, err := fixture.Store.AcknowledgeDue(t.Context(), sender); err == nil {
		t.Fatal("lost reply not reported")
	}
	makeAcknowledgementDue(t, fixture)
	if count, err := fixture.Store.AcknowledgeDue(t.Context(), sender); err != nil || count != 1 || calls.Load() != 2 || reactions.Load() != 1 {
		t.Fatalf("Slack receipt recovery: %d %v calls=%d reactions=%d", count, err, calls.Load(), reactions.Load())
	}
}
