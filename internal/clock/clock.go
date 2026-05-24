// Package clock centralizes wall-clock access for code that needs elapsed time.
package clock

import "time"

// Now returns the current wall-clock time.
func Now() time.Time {
	return time.Now()
}

// Since returns the elapsed time since start.
func Since(start time.Time) time.Duration {
	return time.Since(start)
}
