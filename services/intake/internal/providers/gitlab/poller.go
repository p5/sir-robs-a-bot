package gitlab

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
)

const maxPages = 100

type Poller struct {
	Client *Client
	Policy Policy
	Intake intake.Acceptor
}
type Sweep struct {
	Threads       int
	Accepted      int
	Existing      int
	FailedSources int
	NextPoll      time.Duration `json:"-"`
}

// Scan treats to-dos as thread hints. It reads pending and completed to-dos,
// rescans every note, and never mutates the account's to-do state.
func (poller Poller) Scan(ctx context.Context) (Sweep, error) {
	var sweep Sweep
	if err := poller.Policy.Validate(); err != nil {
		return sweep, err
	}
	if poller.Client == nil || poller.Intake == nil || poller.Client.Connection(poller.Policy.Account.ID) != poller.Policy.Connection {
		return sweep, errors.New("GitLab poller connection mismatch")
	}
	seen := make(map[Receipt]bool)
	var failures []error
	for _, state := range []string{"pending", "done"} {
		err := poller.Client.pages(ctx, "/todos?state="+state, func(raw []byte) error {
			var todos []struct {
				Project struct {
					ID int64 `json:"id"`
				} `json:"project"`
				TargetType string `json:"target_type"`
				Target     struct {
					IID int64 `json:"iid"`
				} `json:"target"`
			}
			if json.Unmarshal(raw, &todos) != nil {
				return errors.New("invalid GitLab to-do response")
			}
			for _, todo := range todos {
				receipt := Receipt{ProjectID: todo.Project.ID, IID: todo.Target.IID}
				switch todo.TargetType {
				case "Issue":
					receipt.Resource = "issues"
				case "MergeRequest":
					receipt.Resource = "merge_requests"
				default:
					continue
				}
				if _, err := receipt.route(1); err != nil {
					failures = append(failures, err)
					continue
				}
				if seen[receipt] {
					continue
				}
				seen[receipt] = true
				sweep.Threads++
				route := fmt.Sprintf("/projects/%d/%s/%d/notes", receipt.ProjectID, receipt.Resource, receipt.IID)
				err := poller.Client.pages(ctx, route, func(raw []byte) error {
					var notes []Note
					if json.Unmarshal(raw, &notes) != nil {
						return errors.New("invalid GitLab note listing")
					}
					for _, note := range notes {
						// REST does not establish the responsible editor. Accept only unedited notes.
						if note.UpdatedAt.IsZero() || !note.UpdatedAt.Equal(note.CreatedAt) {
							continue
						}
						submission, ok, err := poller.Policy.Candidate(receipt, note, time.Now().UTC())
						if err != nil {
							sweep.FailedSources++
							failures = append(failures, err)
							continue
						}
						if !ok {
							continue
						}
						accepted, err := poller.Intake.Accept(ctx, submission)
						if err != nil {
							return err
						}
						if accepted.Created {
							sweep.Accepted++
						} else {
							sweep.Existing++
						}
					}
					return nil
				})
				if err != nil {
					if failure, ok := errors.AsType[*APIError](err); ok {
						sweep.NextPoll = max(sweep.NextPoll, failure.RetryAfter)
						if failure.Status == 429 {
							return err
						}
					}
					if ctx.Err() != nil {
						return ctx.Err()
					}
					failures = append(failures, err)
				}
			}
			return nil
		})
		if err != nil {
			if failure, ok := errors.AsType[*APIError](err); ok {
				sweep.NextPoll = max(sweep.NextPoll, failure.RetryAfter)
			}
			return sweep, errors.Join(append([]error{err}, failures...)...)
		}
	}
	return sweep, errors.Join(failures...)
}

// pages constructs numeric page requests itself. Server-provided URLs cannot
// redirect credentials. Hitting the bound fails visibly without a checkpoint.
func (client *Client) pages(ctx context.Context, route string, visit func([]byte) error) error {
	separator := "?"
	for _, c := range route {
		if c == '?' {
			separator = "&"
			break
		}
	}
	for page := 1; page <= maxPages; page++ {
		raw, header, err := client.send(ctx, http.MethodGet, fmt.Sprintf("%s%sper_page=100&page=%d", route, separator, page), nil)
		if err != nil {
			return err
		}
		if err := visit(raw); err != nil {
			return err
		}
		if header.Get("X-Next-Page") == "" {
			return nil
		}
	}
	return errors.New("GitLab pagination exceeds safety bound")
}
