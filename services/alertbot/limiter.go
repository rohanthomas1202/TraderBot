package alertbot

import (
	"sync"

	"golang.org/x/time/rate"
)

// ChatLimiter enforces per-chat command rate limits.
type ChatLimiter struct {
	mu       sync.Mutex
	limiters map[int64]*rate.Limiter
	rps      rate.Limit
	burst    int
}

// NewChatLimiter creates a limiter allowing rps commands per second with the given burst.
func NewChatLimiter(rps float64, burst int) *ChatLimiter {
	return &ChatLimiter{
		limiters: make(map[int64]*rate.Limiter),
		rps:      rate.Limit(rps),
		burst:    burst,
	}
}

// Allow returns true if the chat has not exceeded its rate limit.
func (cl *ChatLimiter) Allow(chatID int64) bool {
	cl.mu.Lock()
	lim, ok := cl.limiters[chatID]
	if !ok {
		lim = rate.NewLimiter(cl.rps, cl.burst)
		cl.limiters[chatID] = lim
	}
	cl.mu.Unlock()
	return lim.Allow()
}
