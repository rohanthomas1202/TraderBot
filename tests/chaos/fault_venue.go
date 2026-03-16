//go:build chaos

package chaos

import (
	"context"
	"fmt"
	"sync"
	"time"

	"autonomy-platform/internal/models"
	"autonomy-platform/services/execution"
)

// VenueFaultInjector wraps a VenueAdapter and injects configurable failures.
type VenueFaultInjector struct {
	inner execution.VenueAdapter

	mu               sync.RWMutex
	injectTimeout    bool
	timeoutDuration  time.Duration
	injectError      bool
	errorMsg         string
	injectPartial    bool
	partialFillRatio float64 // 0.0–1.0
}

func NewVenueFaultInjector(inner execution.VenueAdapter) *VenueFaultInjector {
	return &VenueFaultInjector{inner: inner}
}

func (v *VenueFaultInjector) InjectTimeout(d time.Duration) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.injectTimeout = true
	v.timeoutDuration = d
}

func (v *VenueFaultInjector) InjectError(msg string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.injectError = true
	v.errorMsg = msg
}

func (v *VenueFaultInjector) InjectPartialFailure(ratio float64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.injectPartial = true
	v.partialFillRatio = ratio
}

func (v *VenueFaultInjector) Reset() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.injectTimeout = false
	v.injectError = false
	v.injectPartial = false
}

func (v *VenueFaultInjector) SubmitOrder(ctx context.Context, order *models.ProposedOrder, internalID string) (*execution.ExchangeAck, error) {
	v.mu.RLock()
	timeout := v.injectTimeout
	timeoutDur := v.timeoutDuration
	hasError := v.injectError
	errMsg := v.errorMsg
	v.mu.RUnlock()

	if timeout {
		select {
		case <-time.After(timeoutDur):
			return nil, fmt.Errorf("venue timeout after %s", timeoutDur)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if hasError {
		return nil, fmt.Errorf("injected venue error: %s", errMsg)
	}
	return v.inner.SubmitOrder(ctx, order, internalID)
}

func (v *VenueFaultInjector) CancelOrder(ctx context.Context, exchangeOrderID string) error {
	v.mu.RLock()
	hasError := v.injectError
	errMsg := v.errorMsg
	v.mu.RUnlock()

	if hasError {
		return fmt.Errorf("injected venue error: %s", errMsg)
	}
	return v.inner.CancelOrder(ctx, exchangeOrderID)
}

func (v *VenueFaultInjector) CancelAll(ctx context.Context) (int, error) {
	v.mu.RLock()
	hasError := v.injectError
	errMsg := v.errorMsg
	v.mu.RUnlock()

	if hasError {
		return 0, fmt.Errorf("injected venue error: %s", errMsg)
	}
	return v.inner.CancelAll(ctx)
}

func (v *VenueFaultInjector) PollFills(ctx context.Context, since time.Time) ([]execution.ExchangeFill, error) {
	v.mu.RLock()
	hasError := v.injectError
	errMsg := v.errorMsg
	v.mu.RUnlock()

	if hasError {
		return nil, fmt.Errorf("injected venue error: %s", errMsg)
	}
	return v.inner.PollFills(ctx, since)
}

func (v *VenueFaultInjector) GetOrderStatus(ctx context.Context, exchangeOrderID string) (*execution.ExchangeOrderStatus, error) {
	v.mu.RLock()
	hasError := v.injectError
	errMsg := v.errorMsg
	v.mu.RUnlock()

	if hasError {
		return nil, fmt.Errorf("injected venue error: %s", errMsg)
	}
	return v.inner.GetOrderStatus(ctx, exchangeOrderID)
}
