package sandbox

import "errors"

// TimeoutError is an episode execution failure, not an isolation failure.
// Cleanup and ownership audits must still succeed independently.
type TimeoutError struct{ Cause error }

func (e *TimeoutError) Error() string {
	detail := "sandbox_timeout: sandbox time limit exceeded"
	if e.Cause != nil {
		detail += ": " + e.Cause.Error()
	}
	return detail
}
func (*TimeoutError) ErrorType() string { return "sandbox_timeout" }
func (e *TimeoutError) Unwrap() error   { return e.Cause }
func (e *TimeoutError) SafeDetail() string {
	detail := e.Error()
	var safe interface{ SafeDetail() string }
	if errors.As(e.Cause, &safe) && safe.SafeDetail() != "" {
		detail += ": " + safe.SafeDetail()
	}
	return detail
}
