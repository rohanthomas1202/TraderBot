package execution

import (
	"testing"

	"autonomy-platform/internal/models"
)

// TestOrderStateMachine_ValidTransitions verifies all legal transitions succeed.
func TestOrderStateMachine_ValidTransitions(t *testing.T) {
	tests := []struct {
		from models.OrderStatus
		to   models.OrderStatus
	}{
		{models.StatusPending, models.StatusOpen},
		{models.StatusPending, models.StatusRejected},
		{models.StatusPending, models.StatusFailed},
		{models.StatusPending, models.StatusFilled},
		{models.StatusOpen, models.StatusPartiallyFilled},
		{models.StatusOpen, models.StatusFilled},
		{models.StatusOpen, models.StatusCancelled},
		{models.StatusOpen, models.StatusExpired},
		{models.StatusPartiallyFilled, models.StatusPartiallyFilled},
		{models.StatusPartiallyFilled, models.StatusFilled},
		{models.StatusPartiallyFilled, models.StatusCancelled},
	}

	for _, tt := range tests {
		if err := models.ValidateTransition(tt.from, tt.to); err != nil {
			t.Errorf("expected valid transition %s → %s, got error: %v", tt.from, tt.to, err)
		}
	}
}

// TestOrderStateMachine_IllegalTransitions verifies invalid transitions are rejected.
func TestOrderStateMachine_IllegalTransitions(t *testing.T) {
	tests := []struct {
		from models.OrderStatus
		to   models.OrderStatus
	}{
		// Terminal states cannot transition
		{models.StatusFilled, models.StatusOpen},
		{models.StatusCancelled, models.StatusOpen},
		{models.StatusRejected, models.StatusOpen},
		{models.StatusExpired, models.StatusOpen},
		{models.StatusFailed, models.StatusOpen},
		// Illegal jumps
		{models.StatusPending, models.StatusCancelled},
		{models.StatusPending, models.StatusPartiallyFilled},
		{models.StatusOpen, models.StatusPending},
		{models.StatusOpen, models.StatusRejected},
	}

	for _, tt := range tests {
		if err := models.ValidateTransition(tt.from, tt.to); err == nil {
			t.Errorf("expected illegal transition %s → %s to fail, but got nil", tt.from, tt.to)
		}
	}
}

// TestTerminalStates verifies terminal state detection.
func TestTerminalStates(t *testing.T) {
	terminal := []models.OrderStatus{
		models.StatusFilled,
		models.StatusCancelled,
		models.StatusRejected,
		models.StatusExpired,
		models.StatusFailed,
	}
	nonTerminal := []models.OrderStatus{
		models.StatusPending,
		models.StatusOpen,
		models.StatusPartiallyFilled,
	}

	for _, s := range terminal {
		if !s.IsTerminal() {
			t.Errorf("expected %s to be terminal", s)
		}
	}
	for _, s := range nonTerminal {
		if s.IsTerminal() {
			t.Errorf("expected %s to be non-terminal", s)
		}
	}
}
