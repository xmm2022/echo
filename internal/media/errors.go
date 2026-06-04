package media

import "errors"

var (
	ErrPolicyDenied        = errors.New("media policy denied")
	ErrInvalidRequest      = errors.New("invalid media request")
	ErrLimitReached        = errors.New("media request limit reached")
	ErrNotFound            = errors.New("media object not found")
	ErrConflict            = errors.New("media request conflict")
	ErrInvalidTransition   = errors.New("invalid media transition")
	ErrMetadataUnavailable = errors.New("metadata unavailable")
)
