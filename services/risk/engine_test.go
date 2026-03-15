package risk

import (
	"testing"
	"time"

	"autonomy-platform/internal/models"
)

// TestHMAC_SignVerifyRoundTrip verifies that a signed approval verifies correctly.
func TestHMAC_SignVerifyRoundTrip(t *testing.T) {
	hmacKey := []byte("test-hmac-key-phase2")

	order := &models.ProposedOrder{
		TraceID:        "hmac-test-001",
		StrategyID:     "test-strategy",
		Venue:          "mock",
		MarketID:       "TEST-MARKET",
		Side:           models.SideBuy,
		Quantity:       10,
		PriceMicros:    400_000,
		NotionalMicros: 4_000_000,
		ProposedAt:     time.Now(),
	}

	approval := &Approval{
		TraceID:          order.TraceID,
		Order:            order,
		Decision:         DecisionApproved,
		Checks:           []CheckResultDetail{{Name: "test", Passed: true}},
		PolicyConfigHash: "policy:test123",
		DecidedAt:        time.Now().UTC(),
	}

	// Create engine just for signing
	e := &Engine{hmacKey: hmacKey}
	approval.HMACSignature = e.signApproval(approval)

	if len(approval.HMACSignature) == 0 {
		t.Fatal("HMAC signature is empty")
	}

	// Verify with the same key
	if !VerifyApproval(approval, hmacKey) {
		t.Fatal("HMAC verification failed for valid approval")
	}
}

// TestHMAC_TamperedPayload verifies that modifying the approval causes verification to fail.
func TestHMAC_TamperedPayload(t *testing.T) {
	hmacKey := []byte("test-hmac-key-phase2")

	order := &models.ProposedOrder{
		TraceID:        "hmac-tamper-001",
		StrategyID:     "test-strategy",
		Venue:          "mock",
		MarketID:       "TEST-MARKET",
		Side:           models.SideBuy,
		Quantity:       10,
		PriceMicros:    400_000,
		NotionalMicros: 4_000_000,
		ProposedAt:     time.Now(),
	}

	approval := &Approval{
		TraceID:          order.TraceID,
		Order:            order,
		Decision:         DecisionApproved,
		PolicyConfigHash: "policy:test123",
		DecidedAt:        time.Now().UTC(),
	}

	e := &Engine{hmacKey: hmacKey}
	approval.HMACSignature = e.signApproval(approval)

	// Tamper with the order quantity
	approval.Order.Quantity = 999
	if VerifyApproval(approval, hmacKey) {
		t.Fatal("HMAC verification should fail for tampered quantity")
	}

	// Restore quantity, tamper with decision
	approval.Order.Quantity = 10
	approval.Decision = DecisionDenied
	if VerifyApproval(approval, hmacKey) {
		t.Fatal("HMAC verification should fail for tampered decision")
	}

	// Restore decision, tamper with price
	approval.Decision = DecisionApproved
	approval.Order.PriceMicros = 999_000
	if VerifyApproval(approval, hmacKey) {
		t.Fatal("HMAC verification should fail for tampered price")
	}
}

// TestHMAC_WrongKey verifies that a different key fails verification.
func TestHMAC_WrongKey(t *testing.T) {
	signKey := []byte("signing-key")
	wrongKey := []byte("wrong-key")

	order := &models.ProposedOrder{
		TraceID:        "hmac-wrongkey-001",
		StrategyID:     "test-strategy",
		Venue:          "mock",
		MarketID:       "TEST-MARKET",
		Side:           models.SideBuy,
		Quantity:       5,
		PriceMicros:    300_000,
		NotionalMicros: 1_500_000,
		ProposedAt:     time.Now(),
	}

	approval := &Approval{
		TraceID:          order.TraceID,
		Order:            order,
		Decision:         DecisionApproved,
		PolicyConfigHash: "policy:abc",
		DecidedAt:        time.Now().UTC(),
	}

	e := &Engine{hmacKey: signKey}
	approval.HMACSignature = e.signApproval(approval)

	if VerifyApproval(approval, wrongKey) {
		t.Fatal("HMAC verification should fail with wrong key")
	}
}

// TestHMAC_DeniedOrderSigning verifies that denied orders are also signed correctly.
func TestHMAC_DeniedOrderSigning(t *testing.T) {
	hmacKey := []byte("test-hmac-key")

	order := &models.ProposedOrder{
		TraceID:        "hmac-denied-001",
		StrategyID:     "test-strategy",
		Venue:          "mock",
		MarketID:       "TEST-MARKET",
		Side:           models.SideSell,
		Quantity:       1,
		PriceMicros:    500_000,
		NotionalMicros: 500_000,
		ProposedAt:     time.Now(),
	}

	approval := &Approval{
		TraceID:          order.TraceID,
		Order:            order,
		Decision:         DecisionDenied,
		PolicyConfigHash: "policy:xyz",
		DecidedAt:        time.Now().UTC(),
	}

	e := &Engine{hmacKey: hmacKey}
	approval.HMACSignature = e.signApproval(approval)

	if !VerifyApproval(approval, hmacKey) {
		t.Fatal("HMAC verification failed for denied order")
	}
}

// TestHMAC_DeterministicSignature verifies same inputs produce same signature.
func TestHMAC_DeterministicSignature(t *testing.T) {
	hmacKey := []byte("deterministic-key")
	decidedAt := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)

	makeApproval := func() *Approval {
		return &Approval{
			TraceID: "det-001",
			Order: &models.ProposedOrder{
				TraceID:        "det-001",
				StrategyID:     "strat",
				Venue:          "mock",
				MarketID:       "MKT",
				Side:           models.SideBuy,
				Quantity:       10,
				PriceMicros:    500_000,
				NotionalMicros: 5_000_000,
			},
			Decision:         DecisionApproved,
			PolicyConfigHash: "policy:hash",
			DecidedAt:        decidedAt,
		}
	}

	e := &Engine{hmacKey: hmacKey}
	a1 := makeApproval()
	a1.HMACSignature = e.signApproval(a1)

	a2 := makeApproval()
	a2.HMACSignature = e.signApproval(a2)

	if len(a1.HMACSignature) != len(a2.HMACSignature) {
		t.Fatal("signatures have different lengths")
	}
	for i := range a1.HMACSignature {
		if a1.HMACSignature[i] != a2.HMACSignature[i] {
			t.Fatal("same inputs produced different signatures — not deterministic")
		}
	}
}
