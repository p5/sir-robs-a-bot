package github

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
)

func TestFailedThreadDoesNotStarveAccessibleRequests(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			floor := time.Now().Add(-time.Hour).UTC()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/graphql":
					var payload struct {
						Variables struct {
							ID string `json:"id"`
						} `json:"variables"`
					}
					if err := json.UnmarshalRead(r.Body, &payload); err != nil {
						t.Error(err)
					}
					fmt.Fprintf(w, `{"data":{"node":{"id":%q,"__typename":"Issue","body":"@sir-robs-a-bot explain this","author":{"databaseId":7},"editor":null}}}`, payload.Variables.ID)

				case "/notifications":
					fmt.Fprintf(w, `[{"repository":{"id":11,"full_name":"p5/test"},"subject":{"type":"Issue","url":%q}},
      {"repository":{"id":11,"full_name":"p5/test"},"subject":{"type":"Issue","url":%q}}]`,
						"http://"+r.Host+"/repos/p5/test/issues/1", "http://"+r.Host+"/repos/p5/test/issues/2")
				case "/repos/p5/test/issues/1":
					if status == http.StatusTooManyRequests {
						w.Header().Set("Retry-After", "123")
					}
					w.WriteHeader(status)
				case "/repos/p5/test/issues/2":
					fmt.Fprintf(w, `{"id":2,"node_id":"I_2","number":2,"body":"@sir-robs-a-bot explain this","user":{"id":7,"type":"User"},"created_at":%q}`, floor.Add(time.Minute).Format(time.RFC3339))
				case "/repos/p5/test/issues/2/comments":
					fmt.Fprint(w, `[]`)
				default:
					t.Errorf("unexpected API request: %s", r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			client, err := NewClient(server.Client(), server.URL, "test")
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			poller := Poller{Client: client, Policy: Policy{Account: User{ID: 42, Login: "sir-robs-a-bot"}, AllowedUsers: map[int64]bool{7: true}, Since: floor}, Intake: intake.AcceptFunc(func(_ context.Context, request intake.Submission) (intake.Acceptance, error) {
				count++
				return intake.Acceptance{Created: true}, nil
			})}
			sweep, err := poller.Scan(t.Context())
			if err == nil || sweep.FailedThreads != 1 {
				t.Fatalf("missing thread failure: %+v %v", sweep, err)
			}
			if status == http.StatusTooManyRequests {
				if count != 0 || sweep.NextPoll != 123*time.Second {
					t.Fatalf("continued after rate limit: %+v", sweep)
				}
			} else if count != 1 || sweep.Accepted != 1 {
				t.Fatalf("accessible request starved: %+v", sweep)
			}
		})
	}
}
