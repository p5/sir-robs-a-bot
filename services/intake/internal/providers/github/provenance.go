package github

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"time"
)

// This is a query, not a mutation. Variables keep source IDs out of query text.
const provenanceQuery = `query SourceProvenance($id: ID!) {
 node(id: $id) {
  id
  __typename
  ... on Comment {
   body
   author { ... on User { databaseId } }
   editor { ... on User { databaseId } }
   lastEditedAt
  }
 }
}`

type actor struct {
	DatabaseID int64 `json:"databaseId"`
}

type provenance struct {
	ID           string     `json:"id"`
	Type         string     `json:"__typename"`
	Body         string     `json:"body"`
	Author       *actor     `json:"author"`
	Editor       *actor     `json:"editor"`
	LastEditedAt *time.Time `json:"lastEditedAt"`
}

// Submitter verifies the current source body and returns its responsible human.
// REST identifies the original author even after another user edits the body.
// Missing provenance or a concurrent edit fails closed and retries next sweep.
func (client *Client) Submitter(ctx context.Context, sourceType string, source Source) (int64, error) {
	if source.NodeID == "" || len(source.NodeID) > 256 || (sourceType != "issue" && sourceType != "comment") {
		return 0, errors.New("GitHub source lacks a valid node identity")
	}
	payload, err := json.Marshal(struct {
		Query     string `json:"query"`
		Variables struct {
			ID string `json:"id"`
		} `json:"variables"`
	}{Query: provenanceQuery, Variables: struct {
		ID string `json:"id"`
	}{source.NodeID}})
	if err != nil {
		return 0, err
	}
	body, header, err := client.send(ctx, http.MethodPost, client.base.String()+"/graphql", bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	var response struct {
		Data struct {
			Node *provenance `json:"node"`
		} `json:"data"`
		Errors []struct {
			Type string `json:"type"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return 0, errors.New("invalid GitHub source provenance response")
	}
	for _, failure := range response.Errors {
		if failure.Type == "RATE_LIMITED" {
			return 0, &APIError{Status: http.StatusOK, RateLimited: true, RetryAfter: retryDelay(header)}
		}
	}
	node := response.Data.Node
	if len(response.Errors) != 0 || node == nil || node.ID != source.NodeID || node.Body != source.Body || node.Author == nil || node.Author.DatabaseID != source.User.ID {
		return 0, errors.New("GitHub source provenance is unavailable or changed")
	}
	if sourceType == "comment" && node.Type != "IssueComment" || sourceType == "issue" && node.Type != "Issue" && node.Type != "PullRequest" {
		return 0, errors.New("GitHub source provenance has an unexpected type")
	}
	if node.Editor != nil {
		if node.Editor.DatabaseID <= 0 {
			return 0, errors.New("GitHub source editor is not an identifiable human")
		}
		return node.Editor.DatabaseID, nil
	}
	if node.LastEditedAt != nil {
		return 0, errors.New("GitHub source has an unknown editor")
	}
	return node.Author.DatabaseID, nil
}
