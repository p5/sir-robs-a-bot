package datastore

import (
	"context"
	"errors"
	"time"
)

// Inspector reads queue metadata without participating in dispatch. Inspection
// is optional for adapters and does not confer authority to complete work.
type Inspector interface {
	List(context.Context, ListRequest) (Page, error)
}

// ListRequest selects a bounded page within one kind. After is an exclusive
// resource ID, not an ownership token. IDs sort by their UTF-8 bytes.
type ListRequest struct {
	Kind  string
	After string
	Limit int
}

// Validate rejects invalid bounds and identifiers before datastore access.
func (request ListRequest) Validate() error {
	if err := ValidateKind(request.Kind); err != nil {
		return err
	}
	if request.Limit < 1 || request.Limit > 100 {
		return errors.New("inspection limit must be between 1 and 100")
	}
	if request.After != "" {
		return (Key{Kind: request.Kind, ID: request.After}).Validate()
	}
	return nil
}

// Entry is a point-in-time observation, not a claim. A nonzero LeaseUntil
// indicates an active claim, including one whose lease has expired. Pending
// includes active and delayed work. An unowned, nonpending entry with LastError
// is suspended; one without LastError is completed. DueAt matters only if pending.
type Entry struct {
	Item       Item
	DueAt      time.Time
	LeaseUntil time.Time
	// NotBefore is a protected scheduling floor. Ordinary enqueue preserves it.
	NotBefore time.Time
}

// Page contains at most the requested limit. Next is an exclusive resource ID
// for the next request with the same kind. Empty Next ends traversal. A nonempty
// Next can lead to an empty final page. Concurrent changes do not form a snapshot;
// inserts before the cursor require a new traversal. Pages never mutate records.
type Page struct {
	Entries []Entry
	Next    string
}
