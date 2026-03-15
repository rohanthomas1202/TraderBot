package data

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"autonomy-platform/internal/events"
	"autonomy-platform/pkg/kalshi"

	"github.com/nats-io/nats.go"
)

func TestLoadKalshiFeedConfig(t *testing.T) {
	yaml := `
markets:
  - kalshi_ticker: "KXBTC-T100K"
    internal_id: "KALSHI-BTC-100K"
    display_name: "Bitcoin above $100K"
    enabled: true
  - kalshi_ticker: "KXFED-RATE"
    internal_id: "KALSHI-FED-CUT"
    display_name: "Fed rate cut"
    enabled: false
poll_interval_ms: 2000
api_base_url: "https://demo-api.kalshi.co/trade-api/v2"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "markets.yaml")
	os.WriteFile(path, []byte(yaml), 0644)

	cfg, err := LoadKalshiFeedConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Markets) != 2 {
		t.Fatalf("expected 2 markets, got %d", len(cfg.Markets))
	}
	if cfg.PollIntervalMs != 2000 {
		t.Errorf("expected poll_interval_ms 2000, got %d", cfg.PollIntervalMs)
	}

	enabled := cfg.EnabledMarkets()
	if len(enabled) != 1 {
		t.Fatalf("expected 1 enabled market, got %d", len(enabled))
	}
	if enabled[0].InternalID != "KALSHI-BTC-100K" {
		t.Errorf("expected KALSHI-BTC-100K, got %s", enabled[0].InternalID)
	}
}

func TestLoadKalshiFeedConfig_DefaultInterval(t *testing.T) {
	yaml := `
markets:
  - kalshi_ticker: "KXTEST"
    internal_id: "TEST"
    display_name: "Test"
    enabled: true
`
	dir := t.TempDir()
	path := filepath.Join(dir, "markets.yaml")
	os.WriteFile(path, []byte(yaml), 0644)

	cfg, err := LoadKalshiFeedConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.PollIntervalMs != 1000 {
		t.Errorf("expected default poll_interval_ms 1000, got %d", cfg.PollIntervalMs)
	}
}

func TestPollMarket_PublishesCorrectData(t *testing.T) {
	// Mock Kalshi API server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := kalshi.OrderbookResponse{
			Orderbook: kalshi.Orderbook{
				Yes: [][]int{{65, 100}, {64, 50}},
				No:  [][]int{{36, 80}, {37, 40}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := kalshi.NewClient(kalshi.Config{
		BaseURL:   srv.URL,
		KeyID:     "test",
		KeySecret: "deadbeef01020304deadbeef01020304",
	})

	// Connect to NATS for real publish verification
	nc, err := nats.Connect("nats://localhost:4222")
	if err != nil {
		t.Skip("NATS not available, skipping publish test")
	}
	defer nc.Close()

	pub, err := events.NewPublisher(nc)
	if err != nil {
		t.Fatalf("create publisher: %v", err)
	}

	ing := NewIngestion(pub)
	mapping := MarketMapping{
		KalshiTicker: "KXBTC-T100K",
		InternalID:   "KALSHI-BTC-100K",
		DisplayName:  "Bitcoin above $100K",
		Enabled:      true,
	}

	ing.pollMarket(context.Background(), client, mapping)

	// Verify data was stored in latest map
	md := ing.GetMarketData("kalshi", "KALSHI-BTC-100K")
	if md == nil {
		t.Fatal("expected market data in latest map")
	}
	if md.Venue != "kalshi" {
		t.Errorf("expected venue 'kalshi', got %q", md.Venue)
	}
	if md.MarketID != "KALSHI-BTC-100K" {
		t.Errorf("expected market ID 'KALSHI-BTC-100K', got %q", md.MarketID)
	}

	// Verify price conversion: best bid = 65 cents → 650,000 micros
	expectedBid := int64(650_000)
	if md.BidPriceMicros != expectedBid {
		t.Errorf("bid = %d, want %d", md.BidPriceMicros, expectedBid)
	}

	// Best ask from No side: 36 cents → 360,000 micros
	expectedAsk := int64(360_000)
	if md.AskPriceMicros != expectedAsk {
		t.Errorf("ask = %d, want %d", md.AskPriceMicros, expectedAsk)
	}

	// Mid price
	expectedMid := (expectedBid + expectedAsk) / 2
	if md.LastPriceMicros != expectedMid {
		t.Errorf("mid = %d, want %d", md.LastPriceMicros, expectedMid)
	}

	// Depth
	if md.BidDepth != 150 { // 100 + 50
		t.Errorf("bid depth = %d, want 150", md.BidDepth)
	}
	if md.AskDepth != 120 { // 80 + 40
		t.Errorf("ask depth = %d, want 120", md.AskDepth)
	}
}

func TestPollMarket_EmptyOrderbook_Skips(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := kalshi.OrderbookResponse{
			Orderbook: kalshi.Orderbook{
				Yes: [][]int{},
				No:  [][]int{},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := kalshi.NewClient(kalshi.Config{
		BaseURL:   srv.URL,
		KeyID:     "test",
		KeySecret: "deadbeef01020304deadbeef01020304",
	})

	nc, err := nats.Connect("nats://localhost:4222")
	if err != nil {
		t.Skip("NATS not available, skipping")
	}
	defer nc.Close()

	pub, _ := events.NewPublisher(nc)
	ing := NewIngestion(pub)

	mapping := MarketMapping{
		KalshiTicker: "KXEMPTY",
		InternalID:   "KALSHI-EMPTY",
		DisplayName:  "Empty market",
		Enabled:      true,
	}

	ing.pollMarket(context.Background(), client, mapping)

	// Should NOT have stored data for empty orderbook
	md := ing.GetMarketData("kalshi", "KALSHI-EMPTY")
	if md != nil {
		t.Error("expected no market data for empty orderbook")
	}
}

func TestPollMarket_APIError_DoesNotCrash(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	client := kalshi.NewClient(kalshi.Config{
		BaseURL:   srv.URL,
		KeyID:     "test",
		KeySecret: "deadbeef01020304deadbeef01020304",
	})

	nc, err := nats.Connect("nats://localhost:4222")
	if err != nil {
		t.Skip("NATS not available, skipping")
	}
	defer nc.Close()

	pub, _ := events.NewPublisher(nc)
	ing := NewIngestion(pub)

	mapping := MarketMapping{
		KalshiTicker: "KXFAIL",
		InternalID:   "KALSHI-FAIL",
		DisplayName:  "Failing market",
		Enabled:      true,
	}

	// Should not panic
	ing.pollMarket(context.Background(), client, mapping)

	md := ing.GetMarketData("kalshi", "KALSHI-FAIL")
	if md != nil {
		t.Error("expected no data after API error")
	}
}

func TestRunKalshiFeed_Cancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := kalshi.OrderbookResponse{
			Orderbook: kalshi.Orderbook{
				Yes: [][]int{{50, 10}},
				No:  [][]int{{51, 10}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := kalshi.NewClient(kalshi.Config{
		BaseURL:   srv.URL,
		KeyID:     "test",
		KeySecret: "deadbeef01020304deadbeef01020304",
	})

	nc, err := nats.Connect("nats://localhost:4222")
	if err != nil {
		t.Skip("NATS not available, skipping")
	}
	defer nc.Close()

	pub, _ := events.NewPublisher(nc)
	ing := NewIngestion(pub)

	// Write a temp config
	yaml := `
markets:
  - kalshi_ticker: "KXTEST"
    internal_id: "KALSHI-TEST"
    display_name: "Test"
    enabled: true
poll_interval_ms: 100
`
	dir := t.TempDir()
	path := filepath.Join(dir, "markets.yaml")
	os.WriteFile(path, []byte(yaml), 0644)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ing.RunKalshiFeed(ctx, client, path)
	}()

	// Let it run for a bit, then cancel
	time.Sleep(350 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunKalshiFeed returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunKalshiFeed did not exit after context cancellation")
	}

	// Verify data was published
	md := ing.GetMarketData("kalshi", "KALSHI-TEST")
	if md == nil {
		t.Error("expected market data after running feed")
	}
}
