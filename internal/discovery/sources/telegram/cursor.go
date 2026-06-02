package telegram

import "fmt"

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
