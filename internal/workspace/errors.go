package workspace

import "errors"

// Sentinel errors for workspace operations. Callers should use errors.Is to match them.
var (
	ErrNotFound          = errors.New("workspace not found")
	ErrAlreadyArchived   = errors.New("workspace already archived")
	ErrInvalidName       = errors.New("invalid workspace name")
	ErrCapacityReached   = errors.New("maximum workspace limit reached")
	ErrDeleteNotConfirmed = errors.New("delete must be confirmed")
)
