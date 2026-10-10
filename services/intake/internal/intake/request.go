// Package intake owns accepted requests and their delivery obligations.
package intake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const Kind = "factory-request"

// Connection identifies the receiving provider account or installation. Account
// is opaque: adapters must use stable IDs, not mutable display names.
type Connection struct {
	Provider string
	Account  string
}

// Source identifies one logical submission within a connection. Scope identifies
// its repository, workspace or tenant. Adapters choose stable Kind and ID values;
// delivery IDs and edit versions must not replace the logical source identity.
type Source struct {
	Connection Connection
	Scope      string
	Kind       string
	ID         string
}

// ReceiptMode declares whether acceptance needs a later provider receipt.
type ReceiptMode string

const (
	ReceiptAsync  ReceiptMode = "async"
	ReceiptInline ReceiptMode = "inline"
	ReceiptNone   ReceiptMode = "none"
)

// Submission is already authorized by its provider adapter. Intake validates
// shape and bounds; it cannot authenticate an external actor from strings alone.
// Metadata holds bounded provider-owned JSON needed to acknowledge the source.
type Submission struct {
	Source      Source
	Author      string
	SubmittedBy string
	Body        string
	Instruction string
	CreatedAt   time.Time
	ObservedAt  time.Time
	Metadata    jsontext.Value
	ReceiptMode ReceiptMode
}

// Request snapshots the first accepted observation. Edits do not replace it.
type Request struct {
	ID string
	Submission
}

type Acceptance struct {
	RequestID string
	Created   bool
}

// Acceptor atomically records an authorized submission and its queue and
// acknowledgement obligations. Repeated source identities return the original ID.
type Acceptor interface {
	Accept(context.Context, Submission) (Acceptance, error)
}

// AcceptFunc adapts a probe or test sink to the same boundary as durable intake.
type AcceptFunc func(context.Context, Submission) (Acceptance, error)

func (accept AcceptFunc) Accept(ctx context.Context, submission Submission) (Acceptance, error) {
	return accept(ctx, submission)
}

func validIdentity(value string) bool {
	return value != "" && len(value) <= 512 && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

func (connection Connection) Validate() error {
	if !validIdentity(connection.Account) || len(connection.Provider) == 0 || len(connection.Provider) > 64 {
		return errors.New("invalid intake connection")
	}
	for _, c := range connection.Provider {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return errors.New("invalid intake provider")
		}
	}
	return nil
}

// RequestID hashes an unambiguous tuple, so delimiters inside opaque IDs cannot
// create collisions. Provider and account prevent cross-connection deduplication.
func (source Source) RequestID() (string, error) {
	if err := source.Connection.Validate(); err != nil {
		return "", err
	}
	if !validIdentity(source.Scope) || !validIdentity(source.Kind) || !validIdentity(source.ID) {
		return "", errors.New("invalid intake source")
	}
	encoded, err := json.Marshal([5]string{source.Connection.Provider, source.Connection.Account, source.Scope, source.Kind, source.ID})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return source.Connection.Provider + ":" + hex.EncodeToString(digest[:]), nil
}

func (submission Submission) Validate() error {
	switch submission.ReceiptMode {
	case ReceiptAsync, ReceiptInline, ReceiptNone:
	default:
		return errors.New("explicit receipt mode is required")
	}
	if _, err := submission.Source.RequestID(); err != nil {
		return err
	}
	if !validIdentity(submission.Author) || !validIdentity(submission.SubmittedBy) || submission.CreatedAt.IsZero() || submission.ObservedAt.IsZero() {
		return errors.New("invalid intake actor or observation time")
	}
	if strings.TrimSpace(submission.Instruction) == "" || len(submission.Instruction) > 16<<10 || len(submission.Body) > 64<<10 || !utf8.ValidString(submission.Body) || !utf8.ValidString(submission.Instruction) || strings.ContainsRune(submission.Body, 0) || strings.ContainsRune(submission.Instruction, 0) {
		return errors.New("invalid intake instruction or body")
	}
	if len(submission.Metadata) == 0 || len(submission.Metadata) > 16<<10 || !jsontext.Value(submission.Metadata).IsValid() {
		return errors.New("invalid intake provider metadata")
	}
	return nil
}

// Validate checks that a stored snapshot still matches its logical source.
func (request Request) Validate() error {
	if err := request.Submission.Validate(); err != nil {
		return err
	}
	id, err := request.Source.RequestID()
	if err != nil || id != request.ID {
		return errors.New("stored request identity mismatch")
	}
	return nil
}
