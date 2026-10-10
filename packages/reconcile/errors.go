package reconcile

import "strings"

// Permanent marks a reconciler error as requiring changed input or an explicit
// wake-up before retrying. A wrapped error retains its identity through errors.Is.
// Permanent(nil) returns nil.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{cause: err}
}

type permanentError struct {
	cause error
}

func (err *permanentError) Error() string {
	return err.cause.Error()
}

func (err *permanentError) Unwrap() error {
	return err.cause
}

// Error implementations can return arbitrary bytes. Persist readable text so a
// malformed diagnostic cannot prevent the store from recording the failure.
func formatFailure(err error) string {
	message := strings.ToValidUTF8(err.Error(), "\uFFFD")
	return "reconcile: " + strings.ReplaceAll(message, "\x00", "\\x00")
}
