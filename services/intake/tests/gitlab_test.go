package factorytests

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/providers/github"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/providers/gitlab"
)

const webhookSecret = "0123456789abcdef0123456789abcdef"

func signGitLab(raw []byte) http.Header {
	header := make(http.Header)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	digest := hmac.New(sha256.New, []byte(webhookSecret))
	fmt.Fprintf(digest, "delivery.%s.", timestamp)
	digest.Write(raw)
	header.Set("webhook-id", "delivery")
	header.Set("webhook-timestamp", timestamp)
	header.Set("webhook-signature", "v1,"+base64.StdEncoding.EncodeToString(digest.Sum(nil)))
	return header
}
func gitlabEvent(floor time.Time, kind string) []byte {
	target := "issue"
	if kind == "MergeRequest" {
		target = "merge_request"
	}
	return fmt.Appendf(nil, `{"object_kind":"note","project_id":11,"user":{"id":7},"object_attributes":{"id":1,"author_id":7,"project_id":11,"noteable_id":90,"noteable_type":%q,"action":"create","note":"@sir-robs-a-bot explain this","created_at":%q},%q:{"id":90,"iid":3}}`, kind, floor.Add(time.Minute).Format(time.RFC3339), target)
}
func gitlabAPI(t *testing.T, floor time.Time) *httptest.Server {
	t.Helper()
	var reacted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "test-secret" {
			t.Error("missing GitLab credential")
		}
		switch {
		case r.URL.Path == "/api/v4/user":
			fmt.Fprint(w, `{"id":42,"username":"sir-robs-a-bot","state":"active"}`)
		case r.URL.Path == "/api/v4/todos":
			fmt.Fprint(w, `[{"project":{"id":11},"target_type":"Issue","target":{"iid":3}},{"project":{"id":11},"target_type":"MergeRequest","target":{"iid":4}}]`)
		case strings.HasSuffix(r.URL.Path, "/award_emoji"):
			if r.URL.Path != "/api/v4/projects/11/issues/3/notes/1/award_emoji" && r.URL.Path != "/api/v4/projects/11/merge_requests/3/notes/1/award_emoji" {
				t.Errorf("wrong receipt coordinates: %s", r.URL.Path)
				w.WriteHeader(404)
				return
			}
			if r.Method == "GET" {
				if reacted.Load() {
					fmt.Fprint(w, `[{"id":80,"name":"eyes","user":{"id":42}}]`)
				} else {
					fmt.Fprint(w, `[]`)
				}
				return
			}
			if r.Method != "POST" {
				t.Error("unexpected GitLab mutation")
				w.WriteHeader(405)
				return
			}
			if err := r.ParseForm(); err != nil || r.Form.Get("name") != "eyes" {
				t.Error("incorrect reaction payload")
			}
			reacted.Store(true)
			w.WriteHeader(201)
			fmt.Fprint(w, `{"id":80,"name":"eyes","user":{"id":42}}`)
		case r.URL.Path == "/api/v4/projects/11/issues/3/notes/1" || r.URL.Path == "/api/v4/projects/11/merge_requests/3/notes/1":
			created := floor.Add(time.Minute).Format(time.RFC3339)
			fmt.Fprintf(w, `{"id":1,"body":"@sir-robs-a-bot explain this","author":{"id":7},"created_at":%q,"updated_at":%q}`, created, created)
		case r.URL.Path == "/api/v4/projects/11/issues/3/notes":
			created := floor.Add(time.Minute).Format(time.RFC3339)
			fmt.Fprintf(w, `[{"id":1,"body":"@sir-robs-a-bot explain this","author":{"id":7},"created_at":%q,"updated_at":%q},{"id":2,"body":"@sir-robs-a-bot unauthorized","author":{"id":99},"created_at":%q,"updated_at":%q},{"id":3,"body":"@sir-robs-a-bot edited","author":{"id":7},"created_at":%q,"updated_at":%q}]`, created, created, created, created, created, floor.Add(2*time.Minute).Format(time.RFC3339))
		case r.URL.Path == "/api/v4/projects/11/merge_requests/4/notes":
			fmt.Fprint(w, `[]`)
		default:
			t.Errorf("unexpected GitLab route: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestGitLabWebhookAndPollingShareDurableRequest(t *testing.T) {
	fixture := open(t)
	api := gitlabAPI(t, fixture.Since)
	client, err := gitlab.NewClient(api.Client(), api.URL, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	connection := client.Connection(42)
	if err := fixture.Store.Activate(t.Context(), connection, fixture.Since); err != nil {
		t.Fatal(err)
	}
	policy := gitlab.Policy{Connection: connection, Account: gitlab.User{ID: 42, Username: "sir-robs-a-bot"}, AllowedUsers: map[int64]bool{7: true}, Since: fixture.Since}
	hook, err := gitlab.NewWebhook(client, policy, fixture.Store, "whsec_"+base64.StdEncoding.EncodeToString([]byte(webhookSecret)))
	if err != nil {
		t.Fatal(err)
	}
	raw := gitlabEvent(fixture.Since, "Issue")
	request := httptest.NewRequest("POST", "/", strings.NewReader(string(raw)))
	request.Header = signGitLab(raw)
	response := httptest.NewRecorder()
	hook.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("webhook failed: %d", response.Code)
	}
	sweep, err := (gitlab.Poller{Client: client, Policy: policy, Intake: fixture.Store}).Scan(t.Context())
	if err != nil || sweep.Accepted != 0 || sweep.Existing != 1 || sweep.Threads != 2 {
		t.Fatalf("poll overlap: %+v %v", sweep, err)
	}
	ids, err := fixture.Store.List(t.Context(), connection)
	if err != nil || len(ids) != 1 {
		t.Fatalf("accepted IDs: %v %v", ids, err)
	}
	if found, err := fixture.Store.DeliverOne(t.Context(), fixture.Queue); err != nil || !found {
		t.Fatalf("queue delivery: %v %v", found, err)
	}
	count, err := fixture.Store.AcknowledgeDue(t.Context(), gitlab.Acknowledger{Client: client, AccountID: 42})
	if err != nil || count != 1 {
		t.Fatalf("eyes acknowledgement: %d %v", count, err)
	}
	status, err := fixture.Store.Acknowledgement(t.Context(), connection, ids[0])
	if err != nil || status.State != "delivered" {
		t.Fatalf("receipt: %+v %v", status, err)
	}
}

func TestGitHubWebhookAndPollingShareDurableRequest(t *testing.T) {
	fixture := open(t)
	var edited atomic.Bool
	api := testAPI(t, fixture.Since, &edited, nil)
	client, err := github.NewClient(api.Client(), api.URL, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	policy := github.Policy{Account: github.User{ID: 42, Login: "sir-robs-a-bot"}, AllowedUsers: map[int64]bool{7: true}, Since: fixture.Since}
	hook, err := github.NewWebhook(client, policy, fixture.Store, webhookSecret)
	if err != nil {
		t.Fatal(err)
	}
	raw := fmt.Appendf(nil, `{"action":"created","repository":{"id":11,"full_name":"p5/test"},"sender":{"id":7,"type":"User"},"issue":{"number":3},"comment":{"id":1,"node_id":"C_1","body":"@sir-robs-a-bot explain this","user":{"id":7,"type":"User"},"created_at":%q}}`, fixture.Since.Add(time.Minute).Format(time.RFC3339))
	digest := hmac.New(sha256.New, []byte(webhookSecret))
	digest.Write(raw)
	request := httptest.NewRequest("POST", "/", strings.NewReader(string(raw)))
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(digest.Sum(nil)))
	request.Header.Set("X-GitHub-Event", "issue_comment")
	response := httptest.NewRecorder()
	hook.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("GitHub webhook: %d", response.Code)
	}
	sweep, err := (github.Poller{Client: client, Policy: policy, Intake: fixture.Store}).Scan(t.Context())
	if err != nil || sweep.Existing != 1 || sweep.Accepted != 0 {
		t.Fatalf("GitHub poll overlap: %+v %v", sweep, err)
	}
	ids, err := fixture.Store.List(t.Context(), github.Connection(42))
	if err != nil || len(ids) != 1 {
		t.Fatalf("duplicate request: %v %v", ids, err)
	}
}

func gitlabCommand(t *testing.T, fixture fixture, api string, args ...string) *exec.Cmd {
	t.Helper()
	command := command(t, fixture, api, append([]string{"-provider", "gitlab"}, args...)...)
	command.Env = append(command.Env, "GITLAB_INGRESS_TOKEN=test-secret", "GITLAB_WEBHOOK_SIGNING_TOKEN=whsec_"+base64.StdEncoding.EncodeToString([]byte(webhookSecret)))
	return command
}

func TestGitLabExecutablePollingAndRecovery(t *testing.T) {
	fixture := open(t)
	api := gitlabAPI(t, fixture.Since)
	for _, args := range [][]string{{"-since", fixture.Since.Format(time.RFC3339), "activate"}, {"scan"}, {"scan"}, {"list"}, {"acknowledge"}} {
		if output, err := gitlabCommand(t, fixture, api.URL, args...).CombinedOutput(); err != nil {
			t.Fatalf("GitLab %v: %v %s", args, err, output)
		}
	}
	client, _ := gitlab.NewClient(api.Client(), api.URL, "test-secret")
	ids, err := fixture.Store.List(t.Context(), client.Connection(42))
	if err != nil || len(ids) != 1 {
		t.Fatalf("CLI accepted IDs: %v %v", ids, err)
	}
	output, err := gitlabCommand(t, fixture, api.URL, "receipt", ids[0]).CombinedOutput()
	var status intake.AcknowledgementStatus
	if err != nil || json.Unmarshal(output, &status) != nil || status.State != "delivered" {
		t.Fatalf("CLI receipt: %s %v", output, err)
	}
}

func TestGitLabExecutableWebhookServer(t *testing.T) {
	fixture := open(t)
	api := gitlabAPI(t, fixture.Since)
	if output, err := gitlabCommand(t, fixture, api.URL, "-since", fixture.Since.Format(time.RFC3339), "activate").CombinedOutput(); err != nil {
		t.Fatalf("activate: %s %v", output, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	process := gitlabCommand(t, fixture, api.URL, "-listen", address, "serve")
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	var processErr error
	go func() {
		processErr = process.Wait()
		close(finished)
	}()
	defer func() {
		process.Process.Signal(syscall.SIGTERM)
		select {
		case <-finished:
			if processErr != nil {
				t.Errorf("server shutdown: %v", processErr)
			}
		case <-time.After(15 * time.Second):
			process.Process.Kill()
			<-finished
			t.Error("server failed to stop")
		}
	}()
	// Observe the listener before sending the authenticated event.
	ready, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			connection.Close()
			break
		}
		select {
		case <-finished:
			t.Fatalf("server exited before readiness: %v", processErr)
		case <-ready.Done():
			t.Fatal("webhook listener did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	raw := gitlabEvent(fixture.Since, "MergeRequest")
	request, err := http.NewRequestWithContext(t.Context(), "POST", "http://"+address, strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header = signGitLab(raw)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("webhook response: %d", response.StatusCode)
	}
	client, _ := gitlab.NewClient(api.Client(), api.URL, "test-secret")
	ids, err := fixture.Store.List(t.Context(), client.Connection(42))
	if err != nil || len(ids) != 1 {
		t.Fatalf("webhook durability: %v %v", ids, err)
	}
	// Independent delivery exercises the MR note endpoint without waiting for a timer.
	count, err := fixture.Store.AcknowledgeDue(t.Context(), gitlab.Acknowledger{Client: client, AccountID: 42})
	if err != nil || count > 1 {
		t.Fatalf("MR acknowledgement: %d %v", count, err)
	}
}

func TestGitHubExecutableWebhookServer(t *testing.T) {
	fixture := open(t)
	var edited atomic.Bool
	api := testAPI(t, fixture.Since, &edited, nil)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	process := command(t, fixture, api.URL, "-listen", address, "serve")
	process.Env = append(process.Env, "GITHUB_WEBHOOK_SECRET="+webhookSecret)
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var processErr error
	go func() {
		processErr = process.Wait()
		close(done)
	}()
	defer func() {
		process.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
			if processErr != nil {
				t.Errorf("GitHub listener shutdown: %v", processErr)
			}
		case <-time.After(15 * time.Second):
			process.Process.Kill()
			<-done
			t.Error("GitHub listener failed to stop")
		}
	}()
	ready, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			connection.Close()
			break
		}
		select {
		case <-done:
			t.Fatalf("GitHub listener exited: %v", processErr)
		case <-ready.Done():
			t.Fatal("GitHub listener did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	raw := fmt.Appendf(nil, `{"action":"created","repository":{"id":11,"full_name":"p5/test"},"sender":{"id":7,"type":"User"},"issue":{"number":3},"comment":{"id":1,"node_id":"C_1","body":"@sir-robs-a-bot explain this","user":{"id":7,"type":"User"},"created_at":%q}}`, fixture.Since.Add(time.Minute).Format(time.RFC3339))
	digest := hmac.New(sha256.New, []byte(webhookSecret))
	digest.Write(raw)
	request, err := http.NewRequestWithContext(t.Context(), "POST", "http://"+address, strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(digest.Sum(nil)))
	request.Header.Set("X-GitHub-Event", "issue_comment")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("GitHub listener response: %d", response.StatusCode)
	}
	ids, err := fixture.Store.List(t.Context(), github.Connection(42))
	if err != nil || len(ids) != 1 {
		t.Fatalf("GitHub listener durability: %v %v", ids, err)
	}
}
