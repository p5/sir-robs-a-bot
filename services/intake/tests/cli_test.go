package factorytests

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/providers/github"
)

func command(t *testing.T, fixture fixture, origin string, args ...string) *exec.Cmd {
	t.Helper()
	binary := os.Getenv("INTAKE_BINARY")
	if binary == "" {
		t.Fatal("INTAKE_BINARY must identify the built Buck artifact")
	}
	command := exec.CommandContext(t.Context(), binary, append([]string{"-storage", "postgres", "-api-url", origin, "-allow-user-ids", "7"}, args...)...)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "INTAKE_DSN=") && !strings.HasPrefix(entry, "GITHUB_INGRESS_TOKEN=") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "INTAKE_DSN="+fixture.DSN, "GITHUB_INGRESS_TOKEN=test-secret")
	return command
}

func runCLI(t *testing.T, fixture fixture, origin string, args ...string) string {
	t.Helper()
	output, err := command(t, fixture, origin, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("factory %v: %v %s", args, err, output)
	}
	return string(output)
}

func testAPI(t *testing.T, floor time.Time, edited *atomic.Bool, secondPage func(http.ResponseWriter, *http.Request), editorIDs ...int64) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			if r.Method != "POST" {
				t.Error("provenance must use a GraphQL query")
			}
			var payload struct {
				Query     string `json:"query"`
				Variables struct {
					ID string `json:"id"`
				} `json:"variables"`
			}
			if err := json.UnmarshalRead(r.Body, &payload); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if !strings.HasPrefix(payload.Query, "query ") || strings.Contains(payload.Query, "mutation") {
				t.Error("unexpected GraphQL operation")
			}
			body := "@sir-robs-a-bot explain this"
			if payload.Variables.ID == "C_5" {
				body = "@sir-robs-a-bot second request"
			} else if edited.Load() {
				body = "@sir-robs-a-bot changed instruction"
			}
			editor := 7
			if len(editorIDs) > 0 {
				editor = int(editorIDs[0])
			}
			fmt.Fprintf(w, `{"data":{"node":{"id":%q,"__typename":"IssueComment","body":%q,"author":{"databaseId":7},"editor":{"databaseId":%d}}}}`, payload.Variables.ID, body, editor)
			return
		}
		if r.Method == "POST" && (r.URL.Path == "/repos/p5/test/issues/comments/1/reactions" || r.URL.Path == "/repos/p5/test/issues/comments/5/reactions") {
			var payload struct {
				Content string `json:"content"`
			}
			if err := json.UnmarshalRead(r.Body, &payload); err != nil || payload.Content != "eyes" {
				t.Errorf("reaction payload: %+v %v", payload, err)
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":80,"content":"eyes","user":{"id":42}}`)
			return
		}
		if r.Method != "GET" {
			t.Errorf("unexpected GitHub write: %s", r.Method)
			w.WriteHeader(405)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("missing API credential")
		}
		switch r.URL.Path {
		case "/user":
			fmt.Fprint(w, `{"id":42,"login":"sir-robs-a-bot","type":"User"}`)
		case "/notifications":
			if r.URL.Query().Get("all") != "true" {
				t.Error("read notifications excluded")
			}
			fmt.Fprintf(w, `[{"reason":"comment","repository":{"id":11,"full_name":"p5/test"},"subject":{"type":"Issue","url":%q}}]`, "http://"+r.Host+"/repos/p5/test/issues/3")
		case "/repos/p5/test/issues/3":
			fmt.Fprint(w, `{"id":100,"number":3,"body":"ordinary issue","user":{"id":7,"type":"User"}}`)
		case "/repos/p5/test/issues/3/comments":
			if r.URL.Query().Get("page") == "2" {
				if secondPage != nil {
					secondPage(w, r)
					return
				}
				fmt.Fprint(w, `[{"id":3,"body":"latest comment has no mention","user":{"id":7,"type":"User"}}]`)
				return
			}
			w.Header().Set("Link", fmt.Sprintf(`<%s%s&page=2>; rel="next"`, "http://"+r.Host, r.URL.RequestURI()))
			body := "@sir-robs-a-bot explain this"
			if edited.Load() {
				body = "@sir-robs-a-bot changed instruction"
			}
			fmt.Fprintf(w, `[{"id":1,"node_id":"C_1","body":%q,"user":{"id":7,"login":"p5","type":"User"},"created_at":%q},
    {"id":2,"body":"@sir-robs-a-bot unauthorized","user":{"id":99,"type":"User"},"created_at":%q},
    {"id":4,"body":"@sir-robs-a-bot historical","user":{"id":7,"type":"User"},"created_at":%q}]`,
				body, floor.Add(time.Minute).Format(time.RFC3339), floor.Add(time.Minute).Format(time.RFC3339), floor.Add(-time.Minute).Format(time.RFC3339))
		default:
			t.Errorf("unexpected API path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestExecutableMentionIntake(t *testing.T) {
	fixture := open(t)
	var edited atomic.Bool
	server := testAPI(t, fixture.Since, &edited, nil)
	runCLI(t, fixture, server.URL, "migrate")
	probe := runCLI(t, fixture, server.URL, "-since", fixture.Since.Format(time.RFC3339), "probe")
	if !strings.Contains(probe, githubRequestID(t, "1")) || strings.Contains(probe, "explain this") {
		t.Fatalf("probe output: %s", probe)
	}
	var count int
	if err := fixture.Store.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM intake_requests`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("probe persisted work: %d %v", count, err)
	}
	first := runCLI(t, fixture, server.URL, "scan")
	if !strings.Contains(first, `"Accepted":1`) || !strings.Contains(first, `"Acknowledged":1`) {
		t.Fatalf("scan did not accept exactly one request: %s", first)
	}
	edited.Store(true)
	second := runCLI(t, fixture, server.URL, "scan")
	if !strings.Contains(second, `"Accepted":0`) || !strings.Contains(second, `"Existing":1`) || !strings.Contains(second, `"Acknowledged":0`) {
		t.Fatalf("replay: %s", second)
	}
	var saved intake.Request
	if err := json.Unmarshal([]byte(runCLI(t, fixture, server.URL, "inspect", githubRequestID(t, "1"))), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Instruction != "explain this" {
		t.Fatalf("edit changed accepted request: %+v", saved)
	}
	if got := runCLI(t, fixture, server.URL, "relay"); !strings.Contains(got, `"Delivered":1`) {
		t.Fatalf("relay: %s", got)
	}
	item, err := fixture.Queue.Get(t.Context(), datastore.Key{Kind: intake.Kind, ID: saved.ID})
	if err != nil || !item.Pending {
		t.Fatalf("queue missing request: %+v %v", item, err)
	}
	if got := runCLI(t, fixture, server.URL, "list"); !strings.Contains(got, saved.ID) {
		t.Fatalf("request absent from list: %s", got)
	}
}

func TestExecutableRejectsWrongAccountAndAllowlist(t *testing.T) {
	fixture := open(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"id":7,"login":"p5","type":"User"}`) }))
	defer server.Close()
	output, err := command(t, fixture, server.URL, "scan").CombinedOutput()
	if err == nil || !strings.Contains(string(output), "different account") {
		t.Fatalf("wrong account: %v %s", err, output)
	}
	var edited atomic.Bool
	correct := testAPI(t, fixture.Since, &edited, nil)
	for _, value := range []string{"", "0", "-1", "7,not-an-id", "9223372036854775808"} {
		output, err := command(t, fixture, correct.URL, "-allow-user-ids", value, "scan").CombinedOutput()
		if err == nil || !strings.Contains(string(output), "positive numeric") {
			t.Fatalf("invalid allowlist %q: %v %s", value, err, output)
		}
	}
}

func TestProcessCrashDuringSweepRecoversAcceptedRequests(t *testing.T) {
	fixture := open(t)
	var edited atomic.Bool
	reached := make(chan struct{}, 1)
	var block atomic.Bool
	block.Store(true)
	server := testAPI(t, fixture.Since, &edited, func(w http.ResponseWriter, r *http.Request) {
		if block.Load() {
			reached <- struct{}{}
			<-r.Context().Done()
			return
		}
		fmt.Fprintf(w, `[{"id":5,"node_id":"C_5","body":"@sir-robs-a-bot second request","user":{"id":7,"type":"User"},"created_at":%q}]`, fixture.Since.Add(time.Minute).Format(time.RFC3339))
	})
	child := command(t, fixture, server.URL, "scan")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if child.ProcessState == nil {
			child.Process.Kill()
			child.Wait()
		}
	})
	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatal("scan did not reach second page")
	}
	if _, err := fixture.Store.Get(t.Context(), githubRequestID(t, "1")); err != nil {
		t.Fatalf("first page not durable: %v", err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("crashed process unexpectedly succeeded")
	}
	block.Store(false)
	output := runCLI(t, fixture, server.URL, "scan")
	if !strings.Contains(output, `"Accepted":1`) || !strings.Contains(output, `"Existing":1`) {
		t.Fatalf("recovery: %s", output)
	}
	ids, err := fixture.Store.List(t.Context(), github.Connection(42))
	if err != nil || len(ids) != 2 {
		t.Fatalf("requests after crash: %v %v", ids, err)
	}
	// A third independent process drains both durable delivery obligations.
	if output := runCLI(t, fixture, server.URL, "relay"); !strings.Contains(output, `"Delivered":`+strconv.Itoa(len(ids))) {
		t.Fatalf("recovered delivery: %s", output)
	}
}

func TestEditedCommentCannotBorrowOriginalAuthorsAuthority(t *testing.T) {
	fixture := open(t)
	var edited atomic.Bool
	server := testAPI(t, fixture.Since, &edited, nil, 99)
	output := runCLI(t, fixture, server.URL, "scan")
	if !strings.Contains(output, `"Accepted":0`) {
		t.Fatalf("unauthorized editor accepted: %s", output)
	}
	ids, err := fixture.Store.List(t.Context(), github.Connection(42))
	if err != nil || len(ids) != 0 {
		t.Fatalf("unauthorized editor stored a request: %v %v", ids, err)
	}
}
