package dailybilling

import "errors"

var (
	// ErrNotConfigured means the new-api database settings are absent.
	ErrNotConfigured = errors.New("new-api data source is not configured")
	// ErrSourceUnavailable means the configured database could not be reached.
	ErrSourceUnavailable = errors.New("new-api data source unavailable")
	// ErrQuery means an aggregate query failed.
	ErrQuery = errors.New("daily billing query failed")
	// ErrInvalidTimezone means the reporting timezone is not an IANA name.
	ErrInvalidTimezone = errors.New("invalid timezone")
	// ErrInvalidRange means the date range is malformed or longer than MaxRangeDays.
	ErrInvalidRange = errors.New("invalid time range")
)
