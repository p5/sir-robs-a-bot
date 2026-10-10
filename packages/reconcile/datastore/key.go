package datastore

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Validate checks the portable resource identity rules. Adapters may impose
// documented size limits. The owning resource module validates URL structure
// and canonicalizes references before creating a Key. Stores preserve ID bytes.
func (key Key) Validate() error {
	if err := ValidateKind(key.Kind); err != nil {
		return err
	}
	if err := validateIdentity(key.ID); err != nil {
		return errors.New("resource ID must be nonblank UTF-8 without control characters")
	}
	return nil
}

func validateIdentity(value string) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) {
		return errors.New("invalid identity")
	}
	if strings.ContainsFunc(value, unicode.IsControl) {
		return errors.New("invalid identity")
	}
	return nil
}

// ValidateKind checks a reconciler dispatch name using the same rules as Key.
// Adapters can impose documented byte limits after this portable check.
func ValidateKind(kind string) error {
	if err := validateIdentity(kind); err != nil {
		return errors.New("resource kind must be nonblank UTF-8 without control characters")
	}
	return nil
}
