// Package gitlab accepts authenticated GitLab events and delivers receipt reactions.
package gitlab

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
)

const maxResponseBytes = 2 << 20

// Client sends credentials only to a configured instance. Payload URLs and
// pagination links never choose a destination, and redirects are disabled.
type Client struct {
	http   *http.Client
	origin string
	token  string
}

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Bot      bool   `json:"bot"`
	State    string `json:"state"`
}

type APIError struct {
	Status     int
	RetryAfter time.Duration
}

func (err *APIError) Error() string { return fmt.Sprintf("GitLab API returned HTTP %d", err.Status) }

func NewClient(client *http.Client, instance, token string) (*Client, error) {
	origin, err := url.Parse(instance)
	if err != nil || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" {
		return nil, errors.New("GitLab instance must be an HTTPS origin")
	}
	loopback := net.ParseIP(origin.Hostname())
	if origin.Scheme != "https" && !(origin.Scheme == "http" && loopback != nil && loopback.IsLoopback()) {
		return nil, errors.New("GitLab requires HTTPS; HTTP loopback is for tests")
	}
	if client == nil || strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("GitLab HTTP client and explicit token are required")
	}
	origin.Host = strings.ToLower(origin.Host)
	if origin.Scheme == "https" && origin.Port() == "443" {
		origin.Host = origin.Hostname()
		if strings.Contains(origin.Host, ":") {
			origin.Host = "[" + origin.Host + "]"
		}
	}
	isolated := *client
	isolated.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{http: &isolated, origin: origin.String(), token: token}, nil
}

// Connection includes the configured instance because numeric IDs are not global.
func (client *Client) Connection(accountID int64) intake.Connection {
	return intake.Connection{Provider: "gitlab", Account: client.origin + "/users/" + strconv.FormatInt(accountID, 10)}
}

func (client *Client) Identity(ctx context.Context) (User, error) {
	raw, _, err := client.send(ctx, http.MethodGet, "/user", nil)
	if err != nil {
		return User{}, err
	}
	var user User
	if err := json.Unmarshal(raw, &user); err != nil || user.ID <= 0 || user.Username == "" || user.State != "active" {
		return User{}, errors.New("invalid GitLab account identity")
	}
	return user, nil
}

func (client *Client) send(ctx context.Context, method, route string, body io.Reader) ([]byte, http.Header, error) {
	request, err := http.NewRequestWithContext(ctx, method, client.origin+"/api/v4"+route, body)
	if err != nil {
		return nil, nil, errors.New("invalid GitLab API request")
	}
	request.Header.Set("PRIVATE-TOKEN", client.token)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	response, err := client.http.Do(request)
	if err != nil {
		return nil, nil, errors.New("GitLab API transport failed")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes {
		return nil, nil, errors.New("invalid or oversized GitLab response")
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		retry := time.Duration(0)
		if seconds, err := strconv.ParseInt(response.Header.Get("Retry-After"), 10, 32); err == nil && seconds > 0 {
			retry = time.Duration(seconds) * time.Second
		} else if date, err := http.ParseTime(response.Header.Get("Retry-After")); err == nil {
			retry = max(0, time.Until(date))
		}
		return nil, response.Header, &APIError{Status: response.StatusCode, RetryAfter: retry}
	}
	return raw, response.Header, nil
}
