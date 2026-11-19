package openbanking

import "time"

const (
	eventBackoff   = 2 * time.Second
	eventBackoffAt = 10 * time.Minute
)

func Backoff(attempts int) time.Duration {
	if attempts >= 20 {
		return eventBackoffAt
	}
	return min(eventBackoff<<attempts, eventBackoffAt)
}
