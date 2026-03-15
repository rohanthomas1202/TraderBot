package kalshi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestCentsToMicros(t *testing.T) {
	tests := []struct {
		cents int
		want  int64
	}{
		{0, 0},
		{1, 10_000},
		{50, 500_000},
		{65, 650_000},
		{100, 1_000_000},
	}
	for _, tt := range tests {
		got := CentsToMicros(tt.cents)
		if got != tt.want {
			t.Errorf("CentsToMicros(%d) = %d, want %d", tt.cents, got, tt.want)
		}
	}
}

func TestBestBid(t *testing.T) {
	tests := []struct {
		name      string
		levels    [][]int
		wantPrice int
		wantDepth int32
	}{
		{"empty", nil, 0, 0},
		{"single", [][]int{{65, 10}}, 65, 10},
		{"multiple", [][]int{{60, 5}, {65, 10}, {62, 3}}, 65, 18},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			price, depth := BestBid(tt.levels)
			if price != tt.wantPrice || depth != tt.wantDepth {
				t.Errorf("BestBid() = (%d, %d), want (%d, %d)", price, depth, tt.wantPrice, tt.wantDepth)
			}
		})
	}
}

func TestBestAsk(t *testing.T) {
	tests := []struct {
		name      string
		levels    [][]int
		wantPrice int
		wantDepth int32
	}{
		{"empty", nil, 0, 0},
		{"single", [][]int{{70, 8}}, 70, 8},
		{"multiple", [][]int{{72, 5}, {70, 10}, {75, 3}}, 70, 18},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			price, depth := BestAsk(tt.levels)
			if price != tt.wantPrice || depth != tt.wantDepth {
				t.Errorf("BestAsk() = (%d, %d), want (%d, %d)", price, depth, tt.wantPrice, tt.wantDepth)
			}
		})
	}
}

func TestSignRequest(t *testing.T) {
	c := &Client{
		config: Config{
			KeyID:     "test-key-id",
			KeySecret: "deadbeef01020304deadbeef01020304",
		},
	}

	req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/trade-api/v2/markets", nil)
	err := c.signRequest(req, "1700000000")
	if err != nil {
		t.Fatalf("signRequest: %v", err)
	}

	if req.Header.Get("KALSHI-ACCESS-KEY") != "test-key-id" {
		t.Error("missing KALSHI-ACCESS-KEY header")
	}
	if req.Header.Get("KALSHI-ACCESS-TIMESTAMP") != "1700000000" {
		t.Error("missing KALSHI-ACCESS-TIMESTAMP header")
	}
	sig := req.Header.Get("KALSHI-ACCESS-SIGNATURE")
	if sig == "" {
		t.Error("missing KALSHI-ACCESS-SIGNATURE header")
	}
	// Signature should be a hex string (64 chars for SHA256)
	if len(sig) != 64 {
		t.Errorf("signature length = %d, want 64", len(sig))
	}
}

func TestGetMarkets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		resp := MarketsResponse{
			Markets: []Market{
				{Ticker: "KXBTC-T100K", Title: "Bitcoin above 100K", Status: "open", YesBid: 65, YesAsk: 67},
				{Ticker: "KXFED-RATE", Title: "Fed rate cut", Status: "open", YesBid: 45, YesAsk: 48},
			},
			Cursor: "next-page",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(Config{
		BaseURL:   srv.URL,
		KeyID:     "test",
		KeySecret: "deadbeef01020304deadbeef01020304",
	})

	resp, err := c.GetMarkets(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("GetMarkets: %v", err)
	}
	if len(resp.Markets) != 2 {
		t.Fatalf("expected 2 markets, got %d", len(resp.Markets))
	}
	if resp.Markets[0].Ticker != "KXBTC-T100K" {
		t.Errorf("expected ticker KXBTC-T100K, got %s", resp.Markets[0].Ticker)
	}
	if resp.Cursor != "next-page" {
		t.Errorf("expected cursor next-page, got %s", resp.Cursor)
	}
}

func TestGetOrderbook(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := OrderbookResponse{
			Orderbook: Orderbook{
				Yes: [][]int{{65, 100}, {64, 50}, {63, 25}},
				No:  [][]int{{35, 80}, {36, 40}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(Config{
		BaseURL:   srv.URL,
		KeyID:     "test",
		KeySecret: "deadbeef01020304deadbeef01020304",
	})

	ob, err := c.GetOrderbook(context.Background(), "KXBTC-T100K")
	if err != nil {
		t.Fatalf("GetOrderbook: %v", err)
	}
	if len(ob.Yes) != 3 {
		t.Errorf("expected 3 yes levels, got %d", len(ob.Yes))
	}

	bidPrice, bidDepth := BestBid(ob.Yes)
	if bidPrice != 65 || bidDepth != 175 {
		t.Errorf("best bid = (%d, %d), want (65, 175)", bidPrice, bidDepth)
	}

	askPrice, askDepth := BestAsk(ob.No)
	if askPrice != 35 || askDepth != 120 {
		t.Errorf("best ask = (%d, %d), want (35, 120)", askPrice, askDepth)
	}
}

func TestHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()

	c := NewClient(Config{
		BaseURL:   srv.URL,
		KeyID:     "test",
		KeySecret: "deadbeef01020304deadbeef01020304",
	})

	_, err := c.GetMarkets(context.Background(), "", 10)
	if err == nil {
		t.Fatal("expected error for 429 response")
	}
}

func TestRateLimiter(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		json.NewEncoder(w).Encode(MarketsResponse{})
	}))
	defer srv.Close()

	c := NewClient(Config{
		BaseURL:   srv.URL,
		KeyID:     "test",
		KeySecret: "deadbeef01020304deadbeef01020304",
	})

	start := time.Now()
	for i := 0; i < 5; i++ {
		c.GetMarkets(context.Background(), "", 1)
	}
	elapsed := time.Since(start)

	// With 10 req/sec and burst=1, 5 requests should take at least ~400ms
	// (first is immediate, then 4 * 100ms waits)
	if elapsed < 350*time.Millisecond {
		t.Errorf("5 requests completed in %v, expected >= 350ms (rate limiter not working)", elapsed)
	}
	if requests != 5 {
		t.Errorf("expected 5 requests, got %d", requests)
	}
}

func TestGetMarkets_Integration(t *testing.T) {
	keyID := os.Getenv("KALSHI_API_KEY_ID")
	keySecret := os.Getenv("KALSHI_API_KEY_SECRET")
	if keyID == "" || keySecret == "" {
		t.Skip("KALSHI_API_KEY_ID and KALSHI_API_KEY_SECRET not set, skipping integration test")
	}

	baseURL := os.Getenv("KALSHI_API_BASE_URL")
	if baseURL == "" {
		baseURL = "https://demo-api.kalshi.co/trade-api/v2"
	}

	c := NewClient(Config{
		BaseURL:   baseURL,
		KeyID:     keyID,
		KeySecret: keySecret,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := c.GetMarkets(ctx, "", 5)
	if err != nil {
		t.Fatalf("GetMarkets: %v", err)
	}
	t.Logf("Got %d markets (cursor: %q)", len(resp.Markets), resp.Cursor)
	for _, m := range resp.Markets {
		t.Logf("  %s: %s (status=%s, yes_bid=%d, yes_ask=%d)",
			m.Ticker, m.Title, m.Status, m.YesBid, m.YesAsk)
	}
}
