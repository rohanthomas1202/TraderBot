package backtest

import (
	"sync"
	"time"
)

// SimClock is an advanceable simulated clock for backtesting.
type SimClock struct {
	mu  sync.RWMutex
	now time.Time
}

// NewSimClock creates a SimClock starting at the given time.
func NewSimClock(start time.Time) *SimClock {
	return &SimClock{now: start}
}

// Now returns the simulated current time.
func (c *SimClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

// Advance moves the clock forward by d.
func (c *SimClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Set sets the clock to t.
func (c *SimClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}
