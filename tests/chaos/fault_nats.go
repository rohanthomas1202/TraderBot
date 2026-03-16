//go:build chaos

package chaos

import (
	"sync"
	"time"

	"autonomy-platform/internal/events"
)

// NATSFaultInjector wraps an events.Publisher and injects failures.
type NATSFaultInjector struct {
	inner *events.Publisher

	mu           sync.RWMutex
	disconnected bool
	delay        time.Duration
}

func NewNATSFaultInjector(inner *events.Publisher) *NATSFaultInjector {
	return &NATSFaultInjector{inner: inner}
}

func (n *NATSFaultInjector) InjectDisconnect() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.disconnected = true
}

func (n *NATSFaultInjector) InjectDelay(d time.Duration) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.delay = d
}

func (n *NATSFaultInjector) Reconnect() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.disconnected = false
	n.delay = 0
}

// Publish wraps the inner publisher with fault injection.
func (n *NATSFaultInjector) Publish(subject string, data interface{}) {
	n.mu.RLock()
	disconnected := n.disconnected
	delay := n.delay
	n.mu.RUnlock()

	if disconnected {
		return // silently drop
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	n.inner.Publish(subject, data)
}
