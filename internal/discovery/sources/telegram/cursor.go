package telegram

import (
	"fmt"
	"time"
)

type Cursor struct {
	LastMessageID   int64
	LastMessageDate int64
}

type FloodWaitError struct {
	Seconds int
}

func (e FloodWaitError) Error() string {
	return fmt.Sprintf("telegram flood wait: %d seconds", e.Seconds)
}

func (e FloodWaitError) RetryAfter() time.Duration {
	if e.Seconds <= 0 {
		return 0
	}
	return time.Duration(e.Seconds) * time.Second
}
