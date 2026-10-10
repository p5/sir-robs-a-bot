package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
)

const testWebhookSecret = "0123456789abcdef0123456789abcdef"

type unavailableProvenance struct{}

func (unavailableProvenance) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":{"node":null}}`))}, nil
}
func webhookFixture(t *testing.T) *Webhook {
	t.Helper()
	client, err := NewClient(&http.Client{Transport: unavailableProvenance{}}, "https://api.github.com", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	policy := Policy{Account: User{ID: 42, Login: "sir-robs-a-bot"}, AllowedUsers: map[int64]bool{7: true}, Since: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	hook, err := NewWebhook(client, policy, intake.AcceptFunc(func(context.Context, intake.Submission) (intake.Acceptance, error) {
		t.Fatal("accepted without current provenance")
		return intake.Acceptance{}, nil
	}), testWebhookSecret)
	if err != nil {
		t.Fatal(err)
	}
	return hook
}
func TestWebhookRejectsUnsignedOversizedAndUnsupportedRequests(t *testing.T) {
	hook := webhookFixture(t)
	for _, test := range []struct {
		method string
		body   string
		status int
	}{{"GET", "", 405}, {"POST", `{}`, 401}, {"POST", strings.Repeat("x", maxWebhookBytes+1), 413}} {
		request := httptest.NewRequest(test.method, "/", strings.NewReader(test.body))
		response := httptest.NewRecorder()
		hook.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("status: %d want %d", response.Code, test.status)
		}
	}
}
func FuzzWebhookSignature(f *testing.F) {
	f.Add([]byte(`{}`), "sha256=invalid")
	f.Fuzz(func(t *testing.T, raw []byte, signature string) {
		if len(raw) > maxWebhookBytes || len(signature) > 16<<10 {
			return
		}
		hook := webhookFixture(t)
		header := make(http.Header)
		header.Set("X-Hub-Signature-256", signature)
		hook.Verify(header, raw)
		digest := hmac.New(sha256.New, []byte(testWebhookSecret))
		digest.Write(raw)
		header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(digest.Sum(nil)))
		if !hook.Verify(header, raw) {
			t.Fatal("valid signature rejected")
		}
		changed := append(append([]byte{}, raw...), ' ')
		if hook.Verify(header, changed) {
			t.Fatal("modified body authenticated")
		}
	})
}
func FuzzWebhookEvent(f *testing.F) {
	f.Add("issue_comment", []byte(`{"action":"created","repository":{"id":11,"full_name":"p5/test"},"sender":{"id":7,"type":"User"},"issue":{"number":3},"comment":{"id":1,"node_id":"C1","body":"@sir-robs-a-bot investigate","user":{"id":7,"type":"User"},"created_at":"2026-10-10T10:00:00Z"}}`))
	f.Add("issues", []byte(`null`))
	f.Fuzz(func(t *testing.T, event string, raw []byte) {
		if len(raw) > maxWebhookBytes || len(event) > 100 {
			return
		}
		_, _, _ = webhookFixture(t).Receive(t.Context(), event, raw)
	})
}
