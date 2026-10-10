package postgres

import (
	"errors"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

func validateClaim(claim datastore.Claim) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	if err := validateKey(claim.Key); err != nil {
		return err
	}
	if claim.Fence == 0 || claim.Fence > math.MaxInt64 {
		return errors.New("claim fence must fit a positive PostgreSQL bigint")
	}
	if claim.Sequence == 0 || claim.Sequence > math.MaxInt64 {
		return errors.New("claim sequence must fit a positive PostgreSQL bigint")
	}
	return nil
}

func validateKey(key datastore.Key) error {
	if err := key.Validate(); err != nil {
		return err
	}
	if len(key.Kind) > 256 {
		return errors.New("resource kind must contain 1 to 256 bytes")
	}
	if len(key.ID) > 1024 {
		return errors.New("resource ID must contain 1 to 1024 bytes")
	}
	return nil
}

func validateNamespace(namespace string) error {
	if strings.TrimSpace(namespace) == "" || len(namespace) > 128 {
		return errors.New("namespace must contain 1 to 128 bytes")
	}
	if !utf8.ValidString(namespace) || strings.ContainsFunc(namespace, unicode.IsControl) {
		return errors.New("namespace must be UTF-8 without control characters")
	}
	return nil
}
