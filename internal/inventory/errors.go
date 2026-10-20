package inventory

import (
	"errors"
	"fmt"
)

var (
	ErrNotFound            = errors.New("not found")
	ErrInsufficientStock   = errors.New("insufficient available stock")
	ErrInvalidState        = errors.New("invalid state transition")
	ErrIdempotencyMismatch = errors.New("idempotency key reused with a different request")
	ErrRequestInProgress   = errors.New("request with this idempotency key is in progress")
)

type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}
