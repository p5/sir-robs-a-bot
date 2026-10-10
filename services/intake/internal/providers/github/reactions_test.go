package github

import (
	"encoding/json/v2"
	"fmt"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
	"net/http"
	"strings"
	"testing"
)

func TestEyesReaction(t *testing.T) {
	for _, sourceType := range []string{"issue", "comment"} {
		for _, status := range []int{http.StatusOK, http.StatusCreated, http.StatusForbidden, http.StatusTooManyRequests} {
			t.Run(fmt.Sprintf("%s/%d", sourceType, status), func(t *testing.T) {
				client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					expected := "/repos/p5/test/issues/3/reactions"
					if sourceType == "comment" {
						expected = "/repos/p5/test/issues/comments/17/reactions"
					}
					if r.Method != "POST" || r.URL.Path != expected {
						t.Errorf("unexpected reaction: %s %s", r.Method, r.URL.Path)
					}
					var body struct {
						Content string `json:"content"`
					}
					if err := json.UnmarshalRead(r.Body, &body); err != nil || body.Content != "eyes" {
						t.Errorf("payload: %+v %v", body, err)
					}
					w.WriteHeader(status)
					fmt.Fprint(w, `{"id":1,"content":"eyes","user":{"id":42}}`)
				}))
				err := client.ReactEyes(t.Context(), "p5/test", sourceType, 17, 3, 42)
				if (err == nil) != (status == 200 || status == 201) {
					t.Fatalf("reaction error: %v", err)
				}
			})
		}
	}
}

func TestEyesReactionRejectsInvalidResponses(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `{"id":1,"content":"heart","user":{"id":42}}`, `{"id":1,"content":"eyes","user":{"id":7}}`} {
		client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		if err := client.ReactEyes(t.Context(), "p5/test", "comment", 1, 3, 42); err == nil {
			t.Fatalf("accepted response %s", body)
		}
	}
}

func FuzzReactionTargets(f *testing.F) {
	f.Add("p5/test", "comment", int64(1), int64(3))
	f.Add("p5/../../evil", "issue", int64(1), int64(3))
	client, _ := NewClient(&http.Client{}, "https://api.github.com", "test")
	f.Fuzz(func(t *testing.T, repository, kind string, sourceID, number int64) {
		endpoint, err := client.reactionEndpoint(repository, kind, sourceID, number)
		if err != nil {
			return
		}
		parsed, err := client.safeURL(endpoint)
		if err != nil || parsed.RawQuery != "" || !strings.HasPrefix(parsed.Path, "/repos/"+repository+"/issues/") || !strings.HasSuffix(parsed.Path, "/reactions") || sourceID <= 0 || number <= 0 || (kind != "comment" && kind != "issue") {
			t.Fatalf("unsafe reaction target %q", endpoint)
		}
	})
}

func TestObserveAcknowledgementUsesOnlyReadsAndMatchesAccount(t *testing.T) {
	for _, account := range []int64{7, 42} {
		client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("observer attempted mutation: %s", r.Method)
			}
			fmt.Fprintf(w, `[{"id":1,"content":"eyes","user":{"id":%d}}]`, account)
		}))
		sender := Acknowledger{Client: client, AccountID: 42}
		request := intake.Request{Submission: intake.Submission{Source: intake.Source{Connection: sender.Connection(), Kind: "comment", ID: "17"}, Metadata: []byte(`{"Repository":"p5/test","IssueNumber":3}`)}}
		observed, err := sender.ObserveAcknowledgement(t.Context(), request)
		if err != nil || observed != (account == 42) {
			t.Fatalf("observation: %t %v", observed, err)
		}
	}
}
