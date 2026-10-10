package gitlab

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

func testPolicy() Policy {
	return Policy{Connection: intake.Connection{Provider: "gitlab", Account: "https://gitlab.example/users/42"}, Account: User{ID: 42, Username: "sir-robs-a-bot"}, AllowedUsers: map[int64]bool{7: true}, Since: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}
func signedHeader(raw []byte, at time.Time) http.Header {
	header := make(http.Header)
	header.Set("webhook-id", "delivery-one")
	header.Set("webhook-timestamp", strconv.FormatInt(at.Unix(), 10))
	digest := hmac.New(sha256.New, testKey)
	fmt.Fprintf(digest, "delivery-one.%s.", header.Get("webhook-timestamp"))
	digest.Write(raw)
	header.Set("webhook-signature", "v1,"+base64.StdEncoding.EncodeToString(digest.Sum(nil)))
	return header
}
func eventJSON() []byte {
	return []byte(`{"object_kind":"note","project_id":11,"user":{"id":7},"object_attributes":{"id":1,"author_id":7,"project_id":11,"noteable_id":90,"noteable_type":"Issue","action":"create","note":"@sir-robs-a-bot investigate","created_at":"2026-10-10T10:00:00Z"},"issue":{"id":90,"iid":3}}`)
}

type noteProvenanceTransport struct{}

func (noteProvenanceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	status := http.StatusOK
	raw := `{"id":1,"body":"@sir-robs-a-bot investigate","author":{"id":7},"created_at":"2026-10-10T10:00:00Z","updated_at":"2026-10-10T10:00:00Z"}`
	if request.URL.Path != "/api/v4/projects/11/issues/3/notes/1" {
		status = http.StatusNotFound
		raw = `{}`
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(raw))}, nil
}

func testHook(t *testing.T, acceptor intake.Acceptor) *Webhook {
	t.Helper()
	client, err := NewClient(&http.Client{Transport: noteProvenanceTransport{}}, "https://gitlab.example", "token")
	if err != nil {
		t.Fatal(err)
	}
	hook, err := NewWebhook(client, testPolicy(), acceptor, "whsec_"+base64.StdEncoding.EncodeToString(testKey))
	if err != nil {
		t.Fatal(err)
	}
	return hook
}
func TestWebhookAuthenticationAndAuthorization(t *testing.T) {
	for _, test := range []struct {
		name           string
		body           []byte
		validSignature bool
		stale          bool
		wantCalls      int
		wantStatus     int
	}{
		{"accepted", eventJSON(), true, false, 1, 200},
		{"unsigned", eventJSON(), false, false, 0, 401},
		{"expired", eventJSON(), true, true, 0, 401},
		{"provenance mismatch", []byte(strings.ReplaceAll(string(eventJSON()), `"id":7`, `"id":8`)), true, false, 0, 503},
		{"unauthorized", []byte(strings.ReplaceAll(strings.ReplaceAll(string(eventJSON()), `"id":7`, `"id":8`), `"author_id":7`, `"author_id":8`)), true, false, 0, 200},
		{"edited", []byte(strings.ReplaceAll(string(eventJSON()), `"create"`, `"update"`)), true, false, 0, 200},
		{"malformed", []byte(`{`), true, false, 0, 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			hook := testHook(t, intake.AcceptFunc(func(_ context.Context, s intake.Submission) (intake.Acceptance, error) {
				calls++
				if err := s.Validate(); err != nil {
					t.Fatal(err)
				}
				return intake.Acceptance{Created: true}, nil
			}))
			request := httptest.NewRequest("POST", "/", strings.NewReader(string(test.body)))
			at := time.Now()
			if test.stale {
				at = at.Add(-10 * time.Minute)
			}
			if test.validSignature {
				request.Header = signedHeader(test.body, at)
			}
			response := httptest.NewRecorder()
			hook.ServeHTTP(response, request)
			if response.Code != test.wantStatus || calls != test.wantCalls {
				t.Fatalf("status=%d calls=%d", response.Code, calls)
			}
		})
	}
}

func FuzzWebhookSignature(f *testing.F) {
	f.Add(eventJSON(), "delivery-one", "1", "v1,invalid")
	f.Fuzz(func(t *testing.T, raw []byte, id, timestamp, signature string) {
		if len(raw) > maxWebhookBytes || len(id)+len(timestamp)+len(signature) > 16<<10 {
			return
		}
		hook := testHook(t, intake.AcceptFunc(func(context.Context, intake.Submission) (intake.Acceptance, error) {
			t.Fatal("signature verification accepted work")
			return intake.Acceptance{}, nil
		}))
		header := http.Header{}
		header.Set("webhook-id", id)
		header.Set("webhook-timestamp", timestamp)
		header.Set("webhook-signature", signature)
		hook.Verify(header, raw, time.Now())
		valid := signedHeader(raw, time.Now())
		if !hook.Verify(valid, raw, time.Now()) {
			t.Fatal("valid signature rejected")
		}
		changed := append(append([]byte{}, raw...), ' ')
		if hook.Verify(valid, changed, time.Now()) {
			t.Fatal("changed body authenticated")
		}
	})
}

func FuzzWebhookEvent(f *testing.F) {
	f.Add(eventJSON())
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > maxWebhookBytes {
			return
		}
		hook := testHook(t, intake.AcceptFunc(func(_ context.Context, s intake.Submission) (intake.Acceptance, error) {
			if err := s.Validate(); err != nil || s.Author != "7" || s.SubmittedBy != "7" || s.Source.Kind != "note" {
				t.Fatalf("unsafe submission: %+v %v", s, err)
			}
			encoded, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			var restored intake.Submission
			if err := json.Unmarshal(encoded, &restored); err != nil || restored.Validate() != nil {
				t.Fatal("submission did not round trip")
			}
			return intake.Acceptance{}, nil
		}))
		_, _, _ = hook.Receive(t.Context(), raw)
	})
}

func FuzzMention(f *testing.F) {
	f.Add("@sir-robs-a-bot investigate")
	f.Fuzz(func(t *testing.T, body string) {
		instruction, ok := ParseMention(body, "sir-robs-a-bot")
		if ok && (strings.TrimSpace(instruction) == "" || len(instruction) > 16<<10 || len(body) > 64<<10) {
			t.Fatal("invalid command accepted")
		}
	})
}

func FuzzNoteObservation(f *testing.F) {
	f.Add([]byte(`{"id":1,"body":"@sir-robs-a-bot investigate","author":{"id":7},"created_at":"2026-10-10T10:00:00Z","updated_at":"2026-10-10T10:00:00Z"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > maxResponseBytes {
			return
		}
		var note Note
		if json.Unmarshal(raw, &note) != nil {
			return
		}
		submission, accepted, err := testPolicy().Candidate(Receipt{ProjectID: 11, Resource: "issues", IID: 3}, note, time.Now())
		if err == nil && accepted && (submission.Validate() != nil || submission.Author != "7" || submission.SubmittedBy != "7" || note.System || note.Author.Bot) {
			t.Fatal("unsafe note observation")
		}
	})
}

func TestSignedWebhookRequiresIndependentNoteProvenance(t *testing.T) {
	// A project maintainer can customize the signed hook template. None of these
	// payload claims may override the authoritative note returned by the API.
	for _, test := range []struct {
		name                   string
		author                 int64
		body, created, updated string
	}{
		{"forged author", 99, "@sir-robs-a-bot investigate", "2026-10-10T10:00:00Z", "2026-10-10T10:00:00Z"},
		{"forged text", 7, "@sir-robs-a-bot different instruction", "2026-10-10T10:00:00Z", "2026-10-10T10:00:00Z"},
		{"edited note", 7, "@sir-robs-a-bot investigate", "2026-10-10T10:00:00Z", "2026-10-10T10:01:00Z"},
		{"forged creation time", 7, "@sir-robs-a-bot investigate", "2025-10-10T10:00:00Z", "2025-10-10T10:00:00Z"},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v4/projects/11/issues/3/notes/1" || r.Method != "GET" {
					t.Errorf("unexpected provenance request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
					return
				}
				fmt.Fprintf(w, `{"id":1,"author":{"id":%d},"body":%q,"created_at":%q,"updated_at":%q}`, test.author, test.body, test.created, test.updated)
			}))
			defer api.Close()
			client, err := NewClient(api.Client(), api.URL, "token")
			if err != nil {
				t.Fatal(err)
			}
			policy := testPolicy()
			policy.Connection = client.Connection(42)
			calls := 0
			hook, err := NewWebhook(client, policy, intake.AcceptFunc(func(context.Context, intake.Submission) (intake.Acceptance, error) {
				calls++
				return intake.Acceptance{}, nil
			}), "whsec_"+base64.StdEncoding.EncodeToString(testKey))
			if err != nil {
				t.Fatal(err)
			}
			raw := eventJSON()
			request := httptest.NewRequest("POST", "/", strings.NewReader(string(raw)))
			request.Header = signedHeader(raw, time.Now())
			response := httptest.NewRecorder()
			hook.ServeHTTP(response, request)
			if response.Code != 503 || calls != 0 {
				t.Fatalf("signed claim bypassed API provenance: status=%d accept calls=%d", response.Code, calls)
			}
		})
	}
}
