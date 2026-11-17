package clock

import "time"

const (
	backoffFirst = time.Second
	backoffMax   = 5 * time.Minute
)

func Backoff(attempts int) time.Duration {
	d := backoffFirst
	for i := 1; i < attempts && d < backoffMax; i++ {
		d *= 2
	}
	return min(d, backoffMax)
}
