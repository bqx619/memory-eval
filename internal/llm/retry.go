package llm

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func sleepContext(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Retry-After accepts HTTP delta-seconds and HTTP-date. Invalid headers use
// exponential backoff; all waits, including server suggestions, obey the cap.
func retryWait(header string, now time.Time, base time.Duration, attempt int, cap time.Duration) time.Duration {
	wait := min(base, cap)
	for i := 0; i < attempt && wait < cap; i++ {
		if wait > cap/2 {
			wait = cap
		} else {
			wait *= 2
		}
	}
	header = strings.TrimSpace(header)
	if seconds, err := strconv.ParseUint(header, 10, 64); err == nil {
		if seconds > uint64(cap/time.Second) {
			return cap
		}
		return min(time.Duration(seconds)*time.Second, cap)
	}
	if date, err := http.ParseTime(header); err == nil {
		return min(max(date.Sub(now), 0), cap)
	}
	return wait
}
