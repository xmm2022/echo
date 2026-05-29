package ingest

import "errors"

var (
	ErrProducerUnauthorized = errors.New("producer tool or flag not in whitelist")
	ErrProducerExitFailed   = errors.New("producer process exited non-zero")
)
