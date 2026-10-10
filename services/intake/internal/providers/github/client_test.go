package github

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(server.Client(), server.URL, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	return client, server
}

func TestNotificationsAndConversationPagination(t *testing.T) {
	requests := 0
	client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("unexpected credentials or method")
		}
		switch r.URL.Path {
		case "/user":
			fmt.Fprint(w, `{"id":42,"login":"sir-robs-a-bot","type":"User"}`)
		case "/notifications":
			if r.URL.Query().Get("all") != "true" || r.URL.Query().Get("participating") != "true" {
				t.Error("read or participating notifications excluded")
			}
			w.Header().Set("X-Poll-Interval", "91")
			if r.URL.Query().Get("page") == "2" {
				fmt.Fprint(w, `[]`)
				return
			}
			w.Header().Set("Link", fmt.Sprintf(`<%s%s&page=2>; rel="next"`, "http://"+r.Host, r.URL.RequestURI()))
			fmt.Fprintf(w, `[{"reason":"author","repository":{"id":11,"full_name":"p5/test"},"subject":{"type":"PullRequest","url":%q}}]`, "http://"+r.Host+"/repos/p5/test/pulls/7")
		case "/repos/p5/test/issues/7":
			fmt.Fprint(w, `{"id":71,"number":7,"body":"body"}`)
		case "/repos/p5/test/issues/7/comments":
			if r.URL.Query().Get("page") == "2" {
				fmt.Fprint(w, `[{"id":73,"body":"second"}]`)
				return
			}
			w.Header().Set("Link", fmt.Sprintf(`<%s%s&page=2>; rel="next"`, "http://"+r.Host, r.URL.RequestURI()))
			fmt.Fprint(w, `[{"id":72,"body":"first"}]`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	identity, err := client.Identity(t.Context())
	if err != nil || identity.ID != 42 {
		t.Fatalf("identity: %+v %v", identity, err)
	}
	var sources []string
	interval, err := client.Notifications(t.Context(), time.Unix(1, 0), func(notification Notification) error {
		return client.Conversation(t.Context(), notification, func(kind string, source Source) error {
			sources = append(sources, fmt.Sprintf("%s:%d:%s", kind, source.ID, source.Body))
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(sources, ",") != "issue:71:body,comment:72:first,comment:73:second" || interval != 91*time.Second || requests != 6 {
		t.Fatalf("sources=%v interval=%v requests=%d", sources, interval, requests)
	}
}

func TestUntrustedLinksNeverReceiveCredentials(t *testing.T) {
	var leaked atomic.Bool
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(true)
		fmt.Fprint(w, `[]`)
	}))
	defer foreign.Close()
	for _, mode := range []string{"pagination", "redirect", "subject"} {
		t.Run(mode, func(t *testing.T) {
			client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					http.Redirect(w, r, foreign.URL, http.StatusFound)
					return
				}
				w.Header().Set("Link", `<`+foreign.URL+`/notifications?page=2>; rel="next"`)
				fmt.Fprint(w, `[]`)
			}))
			var err error
			if mode == "subject" {
				notification := Notification{Repository: Repository{ID: 1, FullName: "p5/test"}}
				notification.Subject.Type = "Issue"
				notification.Subject.URL = foreign.URL + "/repos/p5/test/issues/1"
				err = client.Conversation(t.Context(), notification, func(string, Source) error { return nil })
			} else {
				_, err = client.Notifications(t.Context(), time.Unix(1, 0), func(Notification) error { return nil })
			}
			if err == nil {
				t.Fatal("unsafe link accepted")
			}
		})
	}
	if leaked.Load() {
		t.Fatal("request reached a foreign server")
	}
}

func TestResponseFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		link   string
	}{
		{"unauthorized", 401, `private token test-secret`, ""},
		{"invalid JSON", 200, `[{`, ""},
		{"null list", 200, `null`, ""},
		{"oversized", 200, strings.Repeat(" ", maxResponseBytes+1), ""},
		{"malformed link", 200, `[]`, "invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.link != "" {
					w.Header().Set("Link", test.link)
				}
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
			}))
			_, err := client.Notifications(t.Context(), time.Unix(1, 0), func(Notification) error { return nil })
			if err == nil {
				t.Fatal("invalid response accepted")
			}
			if strings.Contains(err.Error(), "test-secret") {
				t.Fatal("API error leaked response body")
			}
		})
	}
}

func TestRateLimitDelayAndCancellation(t *testing.T) {
	client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "123")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	_, err := client.Notifications(t.Context(), time.Unix(1, 0), func(Notification) error { return nil })
	failure, ok := err.(*APIError)
	if !ok || failure.RetryAfter != 123*time.Second {
		t.Fatalf("rate limit: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.Identity(ctx); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestPaginationCannotLoopOrChangeResource(t *testing.T) {
	for _, mode := range []string{"repeat", "resource"} {
		t.Run(mode, func(t *testing.T) {
			client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path := r.URL.RequestURI()
				if mode == "resource" {
					path = "/user"
				}
				w.Header().Set("Link", fmt.Sprintf(`<%s%s>; rel="next"`, "http://"+r.Host, path))
				fmt.Fprint(w, `[]`)
			}))
			if _, err := client.Notifications(t.Context(), time.Unix(1, 0), func(Notification) error { return nil }); err == nil {
				t.Fatal("invalid continuation accepted")
			}
		})
	}
}

func FuzzAPIReferences(f *testing.F) {
	for _, seed := range []string{"https://api.github.com/repos/p5/test/issues/1", "https://evil.invalid/repos/p5/test/issues/1", "https://api.github.com@evil.invalid/", "https://api.github.com/repos/../test/issues/1"} {
		f.Add(seed)
	}
	client, _ := NewClient(&http.Client{}, "https://api.github.com", "test")
	f.Fuzz(func(t *testing.T, raw string) {
		parsed, err := client.safeURL(raw)
		if err == nil && (parsed.Host != "api.github.com" || parsed.Scheme != "https" || parsed.User != nil || strings.Contains(parsed.Path, "/../")) {
			t.Fatal("unsafe reference accepted")
		}
	})
}

func FuzzPagination(f *testing.F) {
	for _, seed := range []string{`<https://api.github.com/notifications?page=2>; rel="next"`, "invalid", `<x>; rel="next", <y>; rel="next"`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > maxResponseBytes {
			return
		}
		_, _ = nextPage([]string{raw})
	})
}

// A fake transport keeps arbitrary API responses local while exercising the
// same bounded decoder and conversation discovery used by the executable.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (transport roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func FuzzGitHubResponses(f *testing.F) {
	for _, seed := range []string{`{"data":{"node":{"id":"C_1","__typename":"IssueComment","body":"instruction","author":{"databaseId":7},"editor":null}}}`, `[]`, `{"id":42,"login":"sir-robs-a-bot","type":"User"}`, `[{"repository":{"id":11,"full_name":"p5/test"},"subject":{"type":"Issue","url":"https://api.github.com/repos/p5/test/issues/1"}}]`, `null`, `{"id":9223372036854775808}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > maxResponseBytes+1 {
			return
		}
		httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw)), Request: request}, nil
		})}
		client, err := NewClient(httpClient, "https://api.github.com", "test")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = client.Submitter(t.Context(), "comment", Source{NodeID: "C_1", Body: "instruction", User: User{ID: 7}})
		_ = client.ReactEyes(t.Context(), "p5/test", "comment", 1, 3, 42)
		_, _ = client.Identity(t.Context())
		_, _ = client.Notifications(t.Context(), time.Unix(1, 0), func(notification Notification) error {
			return client.Conversation(t.Context(), notification, func(string, Source) error { return nil })
		})
	})
}
