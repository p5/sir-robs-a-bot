package dynamodb

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

// MaxFailureBytes bounds stored failure text independently of payloads.
const MaxFailureBytes = 16 * 1024

func validateTable(table string) error {
	if len(table) < 3 || len(table) > 255 {
		return errors.New("table name must contain 3 to 255 bytes")
	}
	for _, char := range table {
		if char != '_' && char != '-' && char != '.' && !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') {
			return errors.New("table name may contain only letters, digits, underscores, hyphens, and periods")
		}
	}
	return nil
}

func validateNamespace(namespace string) error {
	if len(namespace) > 128 || strings.TrimSpace(namespace) == "" {
		return errors.New("namespace must contain 1 to 128 bytes")
	}
	if !utf8.ValidString(namespace) || strings.ContainsFunc(namespace, unicode.IsControl) {
		return errors.New("namespace must be UTF-8 without control characters")
	}
	return nil
}

func validateKey(key datastore.Key) error {
	if err := key.Validate(); err != nil {
		return err
	}
	if len(key.Kind) > 256 || len(key.ID) > 1024 {
		return errors.New("resource kind must fit 256 bytes and ID must fit 1024 bytes")
	}
	return nil
}

func validateCompletion(completion datastore.Completion) error {
	if err := completion.Validate(); err != nil {
		return err
	}
	if len(completion.Failure) > MaxFailureBytes {
		return errors.New("failure text exceeds the DynamoDB adapter limit of 16 KiB")
	}
	return nil
}
