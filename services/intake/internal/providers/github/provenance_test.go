package github

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestSourceProvenanceFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name      string
		body      string
		submitter int64
	}{
		{"original author", `{"data":{"node":{"id":"C_1","__typename":"IssueComment","body":"instruction","author":{"databaseId":7},"editor":null}}}`, 7},
		{"different editor", `{"data":{"node":{"id":"C_1","__typename":"IssueComment","body":"instruction","author":{"databaseId":7},"editor":{"databaseId":99}}}}`, 99},
		{"body changed", `{"data":{"node":{"id":"C_1","__typename":"IssueComment","body":"different instruction","author":{"databaseId":7}}}}`, 0},
		{"identity changed", `{"data":{"node":{"id":"C_2","__typename":"IssueComment","body":"instruction","author":{"databaseId":7}}}}`, 0},
		{"author changed", `{"data":{"node":{"id":"C_1","__typename":"IssueComment","body":"instruction","author":{"databaseId":99}}}}`, 0},
		{"wrong type", `{"data":{"node":{"id":"C_1","__typename":"Issue","body":"instruction","author":{"databaseId":7}}}}`, 0},
		{"unknown editor", `{"data":{"node":{"id":"C_1","__typename":"IssueComment","body":"instruction","author":{"databaseId":7},"lastEditedAt":"2026-10-10T01:00:00Z"}}}`, 0},
		{"partial error", `{"errors":[{"type":"FORBIDDEN"}],"data":{"node":{"id":"C_1","__typename":"IssueComment","body":"instruction","author":{"databaseId":7}}}}`, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, test.body) }))
			submitter, err := client.Submitter(t.Context(), "comment", Source{NodeID: "C_1", Body: "instruction", User: User{ID: 7}})
			if submitter != test.submitter || (err == nil) != (test.submitter != 0) {
				t.Fatalf("submitter %d, error %v", submitter, err)
			}
		})
	}
}

func TestGraphQLRateLimitUsesRetryDelay(t *testing.T) {
	client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "123")
		fmt.Fprint(w, `{"errors":[{"type":"RATE_LIMITED"}]}`)
	}))
	_, err := client.Submitter(t.Context(), "comment", Source{NodeID: "C_1", Body: "instruction", User: User{ID: 7}})
	failure, ok := err.(*APIError)
	if !ok || !failure.RateLimited || failure.RetryAfter != 123*time.Second {
		t.Fatalf("GraphQL rate limit: %v", err)
	}
}
