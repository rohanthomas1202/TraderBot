package recon

import (
	"testing"
)

func TestComparePositionMaps_Match(t *testing.T) {
	internal := map[string]int32{
		"BTC-USD": 10,
		"ETH-USD": -5,
	}
	exchange := map[string]int32{
		"BTC-USD": 10,
		"ETH-USD": -5,
	}

	mismatches := comparePositionMaps(internal, exchange)
	if len(mismatches) != 0 {
		t.Errorf("expected no mismatches, got %d: %+v", len(mismatches), mismatches)
	}
}

func TestComparePositionMaps_QuantityMismatch(t *testing.T) {
	internal := map[string]int32{
		"BTC-USD": 10,
	}
	exchange := map[string]int32{
		"BTC-USD": 8,
	}

	mismatches := comparePositionMaps(internal, exchange)
	if len(mismatches) != 1 {
		t.Fatalf("expected 1 mismatch, got %d", len(mismatches))
	}
	if mismatches[0].Field != "position" || mismatches[0].Key != "BTC-USD" {
		t.Errorf("unexpected mismatch: %+v", mismatches[0])
	}
}

func TestComparePositionMaps_MissingOnExchange(t *testing.T) {
	internal := map[string]int32{
		"BTC-USD": 10,
	}
	exchange := map[string]int32{}

	mismatches := comparePositionMaps(internal, exchange)
	if len(mismatches) != 1 {
		t.Fatalf("expected 1 mismatch, got %d", len(mismatches))
	}
	if mismatches[0].Exchange != "0 (missing)" {
		t.Errorf("expected missing exchange, got: %s", mismatches[0].Exchange)
	}
}

func TestComparePositionMaps_MissingInternal(t *testing.T) {
	internal := map[string]int32{}
	exchange := map[string]int32{
		"BTC-USD": 5,
	}

	mismatches := comparePositionMaps(internal, exchange)
	if len(mismatches) != 1 {
		t.Fatalf("expected 1 mismatch, got %d", len(mismatches))
	}
	if mismatches[0].Internal != "0 (missing)" {
		t.Errorf("expected missing internal, got: %s", mismatches[0].Internal)
	}
}

func TestComparePositionMaps_EmptyBothSides(t *testing.T) {
	mismatches := comparePositionMaps(map[string]int32{}, map[string]int32{})
	if len(mismatches) != 0 {
		t.Errorf("expected no mismatches for empty maps, got %d", len(mismatches))
	}
}

func TestComparePositionMaps_ZeroExchangePosition(t *testing.T) {
	// Zero positions on exchange should not be reported as mismatches
	// when internal also doesn't have them
	internal := map[string]int32{}
	exchange := map[string]int32{
		"BTC-USD": 0,
	}

	mismatches := comparePositionMaps(internal, exchange)
	if len(mismatches) != 0 {
		t.Errorf("expected no mismatches for zero exchange position, got %d: %+v", len(mismatches), mismatches)
	}
}

func TestStatusesAlign(t *testing.T) {
	tests := []struct {
		internal string
		exchange string
		expect   bool
	}{
		{"open", "open", true},
		{"partially_filled", "partially_filled", true},
		{"open", "filled", false},
		{"pending", "open", false},
	}

	for _, tt := range tests {
		got := statusesAlign(tt.internal, tt.exchange)
		if got != tt.expect {
			t.Errorf("statusesAlign(%q, %q) = %v, want %v", tt.internal, tt.exchange, got, tt.expect)
		}
	}
}
