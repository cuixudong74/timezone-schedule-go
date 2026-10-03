package engine

import "errors"

// 可区分的错误原因。
var (
	ErrNotFound        = errors.New("schedule not found")
	ErrUnauthorized    = errors.New("schedule belongs to another owner")
	ErrInvalidTimezone = errors.New("invalid timezone identifier")
	ErrInvalidRule     = errors.New("invalid recurrence rule")
	ErrInvalidTime     = errors.New("invalid time input")
	ErrCancelled       = errors.New("schedule is cancelled")
)
