package datastore

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// Validate rejects malformed claims. It does not prove ownership. The store
// must still check the fence and active claim atomically against its current state.
func (claim Claim) Validate() error {
	if err := claim.Key.Validate(); err != nil {
		return err
	}
	if claim.Fence == 0 {
		return errors.New("claim fence must be positive")
	}
	if claim.Sequence == 0 {
		return errors.New("claim sequence must be positive")
	}
	return nil
}

// Validate checks scheduling flags and failure text. Failure must be UTF-8
// without NUL bytes.
func (completion Completion) Validate() error {
	if completion.ProtectDelay && (!completion.Again || completion.After <= 0 || completion.Stop) {
		return errors.New("protected delay requires a positive follow-up delay")
	}
	if completion.After < 0 {
		return errors.New("follow-up delay must not be negative")
	}
	if !completion.Again && completion.After != 0 {
		return errors.New("follow-up delay requires a follow-up request")
	}
	if completion.Stop && completion.Again {
		return errors.New("completion cannot stop and request another call")
	}
	if !utf8.ValidString(completion.Failure) || strings.ContainsRune(completion.Failure, 0) {
		return errors.New("failure must be UTF-8 text without NUL bytes")
	}
	return nil
}
