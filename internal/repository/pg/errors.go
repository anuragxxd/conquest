package pg

import "errors"

// Sentinel errors for repository operations.
var (
	// ErrNotFound is returned when the requested entity does not exist.
	ErrNotFound = errors.New("not found")

	// ErrConcurrentUpdate is returned when an optimistic-locking update
	// finds that the row's state has already changed (0 rows affected).
	ErrConcurrentUpdate = errors.New("concurrent update: state already changed")
)
