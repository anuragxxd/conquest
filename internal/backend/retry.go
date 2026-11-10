package backend

import "errors"

// RetryableError marks a failed submission that certainly created nothing
// that could run, so the same task may be submitted again later (e.g. the
// Kubernetes API timed out before creating the Job, or every remote endpoint
// refused the task). Errors without it may have created the task somewhere;
// submitting again could run it twice.
type RetryableError struct{ Err error }

func (e *RetryableError) Error() string { return e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }

// Retryable wraps err as a RetryableError; nil stays nil.
func Retryable(err error) error {
	if err == nil {
		return nil
	}
	return &RetryableError{Err: err}
}

// IsRetryable reports whether err, or an error it wraps, is retryable.
func IsRetryable(err error) bool {
	var re *RetryableError
	return errors.As(err, &re)
}
