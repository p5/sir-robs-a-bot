// Package github reads account notifications and their source conversations.
package github

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const maxResponseBytes = 2 << 20
const maxPages = 100

// Client never follows redirects or links outside its configured API origin.
// Production callers use https://api.github.com. Tests supply a local origin.
type Client struct {
	http  *http.Client
	base  *url.URL
	token string
}

type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"`
}

type Repository struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
}

type Notification struct {
	Repository Repository `json:"repository"`
	Subject    struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"subject"`
}

// Source is the current API observation, not an immutable GitHub event.
type Source struct {
	NodeID    string    `json:"node_id"`
	ID        int64     `json:"id"`
	Number    int64     `json:"number"`
	Body      string    `json:"body"`
	User      User      `json:"user"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	HTMLURL   string    `json:"html_url"`
}

type APIError struct {
	Status      int
	RetryAfter  time.Duration
	RateLimited bool
}

func (err *APIError) Error() string {
	// Response bodies and request URLs may contain private data. Do not log them.
	return fmt.Sprintf("GitHub API returned HTTP %d", err.Status)
}

func NewClient(client *http.Client, baseURL, token string) (*Client, error) {
	base, err := url.Parse(baseURL)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Path != "" || (base.Scheme != "https" && base.Scheme != "http") {
		return nil, errors.New("GitHub API base must be an HTTP origin without credentials or path")
	}
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("GitHub token is empty or invalid")
	}
	if client == nil {
		return nil, errors.New("GitHub HTTP client is required")
	}
	isolated := *client
	isolated.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{http: &isolated, base: base, token: token}, nil
}

func (client *Client) Identity(ctx context.Context) (User, error) {
	var user User
	_, err := client.get(ctx, client.base.String()+"/user", &user)
	if err != nil {
		return User{}, err
	}
	if user.ID <= 0 || user.Login == "" || user.Type != "User" {
		return User{}, errors.New("GitHub credential must identify a human or machine user account")
	}
	return user, nil
}

// Notifications includes read threads and does not filter by persistent reason.
// Every sweep starts from the fixed activation floor. No mutable offset becomes
// a durable checkpoint that could silently skip a thread during pagination.
func (client *Client) Notifications(ctx context.Context, since time.Time, visit func(Notification) error) (time.Duration, error) {
	endpoint := client.base.String() + "/notifications?all=true&participating=true&per_page=50&since=" + url.QueryEscape(since.UTC().Format(time.RFC3339))
	var interval time.Duration
	err := client.pages(ctx, endpoint, func(raw []byte, header http.Header) error {
		if text := header.Get("X-Poll-Interval"); text != "" {
			seconds, err := strconv.ParseUint(text, 10, 32)
			if err != nil || seconds == 0 {
				return errors.New("invalid GitHub polling interval")
			}
			interval = max(interval, time.Duration(seconds)*time.Second)
		}
		var entries []Notification
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return errors.New("null GitHub notifications response")
		}
		if err := json.Unmarshal(raw, &entries); err != nil {
			return errors.New("invalid GitHub notifications response")
		}
		for _, entry := range entries {
			if err := visit(entry); err != nil {
				return err
			}
		}
		return nil
	})
	return max(interval, time.Minute), err
}

// Conversation reads the issue or pull request body and every issue comment.
// Inline review comments are deliberately outside this first ingress contract.
func (client *Client) Conversation(ctx context.Context, notification Notification, visit func(string, Source) error) error {
	if notification.Subject.Type != "Issue" && notification.Subject.Type != "PullRequest" {
		return nil
	}
	repository := notification.Repository
	if repository.ID <= 0 || !validRepository(repository.FullName) {
		return errors.New("invalid GitHub repository identity")
	}
	resource, err := client.safeURL(notification.Subject.URL)
	if err != nil {
		return err
	}
	prefix := "/repos/" + repository.FullName + "/"
	suffix, ok := strings.CutPrefix(resource.Path, prefix)
	if !ok {
		return errors.New("notification subject does not belong to its repository")
	}
	kind, number, ok := strings.Cut(suffix, "/")
	id, parseErr := strconv.ParseInt(number, 10, 64)
	if !ok || parseErr != nil || id <= 0 || (kind != "issues" && kind != "pulls") || resource.RawQuery != "" {
		return errors.New("invalid GitHub conversation reference")
	}
	issueURL := client.base.String() + prefix + "issues/" + strconv.FormatInt(id, 10)
	var issue Source
	if _, err := client.get(ctx, issueURL, &issue); err != nil {
		return err
	}
	if issue.ID <= 0 || issue.Number != id {
		return errors.New("GitHub issue identity does not match its reference")
	}
	if err := visit("issue", issue); err != nil {
		return err
	}
	return client.pages(ctx, issueURL+"/comments?per_page=100", func(raw []byte, _ http.Header) error {
		var comments []Source
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return errors.New("null GitHub comments response")
		}
		if err := json.Unmarshal(raw, &comments); err != nil {
			return errors.New("invalid GitHub comments response")
		}
		for _, comment := range comments {
			if comment.ID <= 0 {
				return errors.New("invalid GitHub comment identity")
			}
			comment.Number = id
			if err := visit("comment", comment); err != nil {
				return err
			}
		}
		return nil
	})
}

func validRepository(name string) bool {
	owner, repo, ok := strings.Cut(name, "/")
	if !ok || owner == "" || repo == "" || owner == "." || owner == ".." || repo == "." || repo == ".." {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-._/", r)) {
			return false
		}
	}
	return !strings.Contains(repo, "/")
}

func (client *Client) safeURL(raw string) (*url.URL, error) {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Scheme != client.base.Scheme || endpoint.Host != client.base.Host || endpoint.User != nil || endpoint.Fragment != "" || endpoint.RawPath != "" || path.Clean(endpoint.Path) != endpoint.Path || strings.ContainsAny(endpoint.Path, "\\\r\n") {
		return nil, errors.New("GitHub API reference is outside the configured origin or has an unsafe path")
	}
	return endpoint, nil
}

func (client *Client) get(ctx context.Context, endpoint string, value any) (http.Header, error) {
	body, header, err := client.read(ctx, endpoint)
	if err != nil {
		return header, err
	}
	if err := json.Unmarshal(body, value); err != nil {
		return header, errors.New("invalid GitHub JSON response")
	}
	return header, nil
}

func (client *Client) read(ctx context.Context, endpoint string) ([]byte, http.Header, error) {
	return client.send(ctx, http.MethodGet, endpoint, nil)
}

func (client *Client) send(ctx context.Context, method, endpoint string, body io.Reader, acceptedStatuses ...int) ([]byte, http.Header, error) {
	if _, err := client.safeURL(endpoint); err != nil {
		return nil, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, nil, errors.New("construct GitHub request")
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	request.Header.Set("User-Agent", "sir-robs-a-bot-factory")
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, errors.New("GitHub HTTP request failed")
	}
	defer response.Body.Close()
	accepted := response.StatusCode == http.StatusOK
	for _, status := range acceptedStatuses {
		accepted = accepted || response.StatusCode == status
	}
	if !accepted {

		return nil, response.Header, &APIError{Status: response.StatusCode, RetryAfter: retryDelay(response.Header), RateLimited: response.StatusCode == http.StatusTooManyRequests || response.Header.Get("Retry-After") != "" || response.Header.Get("X-RateLimit-Remaining") == "0"}
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, response.Header, errors.New("read GitHub response")
	}
	if len(responseBody) > maxResponseBytes {
		return nil, response.Header, errors.New("GitHub response exceeds size limit")
	}
	return responseBody, response.Header, nil
}

func (client *Client) pages(ctx context.Context, endpoint string, visit func([]byte, http.Header) error) error {
	first, err := client.safeURL(endpoint)
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for range maxPages {
		if seen[endpoint] {
			return errors.New("GitHub pagination repeated a page")
		}
		seen[endpoint] = true
		body, header, err := client.read(ctx, endpoint)
		if err != nil {
			return err
		}
		if err := visit(body, header); err != nil {
			return err
		}
		next, err := nextPage(header.Values("Link"))
		if err != nil {
			return err
		}
		if next == "" {
			return nil
		}
		parsed, err := client.safeURL(next)
		if err != nil {
			return err
		}
		firstQuery := first.Query()
		nextQuery := parsed.Query()
		firstQuery.Del("page")
		nextQuery.Del("page")
		if parsed.Path != first.Path || firstQuery.Encode() != nextQuery.Encode() {
			return errors.New("GitHub pagination changed the resource")
		}
		endpoint = next
	}
	return errors.New("GitHub pagination exceeds page limit")
}

func nextPage(headers []string) (string, error) {
	var next string
	for _, header := range headers {
		for part := range strings.SplitSeq(header, ",") {
			reference, attributes, ok := strings.Cut(strings.TrimSpace(part), ";")
			if !ok {
				return "", errors.New("invalid GitHub pagination link")
			}
			if !strings.Contains(attributes, `rel="next"`) {
				continue
			}
			if next != "" || !strings.HasPrefix(reference, "<") || !strings.HasSuffix(reference, ">") {
				return "", errors.New("ambiguous GitHub next page")
			}
			next = strings.TrimSuffix(strings.TrimPrefix(reference, "<"), ">")
		}
	}
	return next, nil
}

func retryDelay(header http.Header) time.Duration {
	delay := time.Minute
	if seconds, err := strconv.ParseUint(header.Get("Retry-After"), 10, 32); err == nil {
		delay = max(delay, time.Duration(seconds)*time.Second)
	} else if retryAt, err := http.ParseTime(header.Get("Retry-After")); err == nil {
		delay = max(delay, time.Until(retryAt))
	}
	if seconds, err := strconv.ParseUint(header.Get("X-Poll-Interval"), 10, 32); err == nil {
		delay = max(delay, time.Duration(seconds)*time.Second)
	}
	if reset, err := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64); err == nil && header.Get("X-RateLimit-Remaining") == "0" {
		delay = max(delay, time.Until(time.Unix(reset, 0)))
	}
	return delay
}
