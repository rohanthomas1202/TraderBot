package kalshi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	c, err := newTestClientWithKey("https://api.example.com/trade-api/v2")
	if err != nil {
		t.Fatalf("create test client: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/trade-api/v2/markets", nil)
	err = c.signRequest(req, "1700000000000")
	if err != nil {
		t.Fatalf("signRequest: %v", err)
	}

	if req.Header.Get("KALSHI-ACCESS-KEY") != "test-key" {
		t.Error("missing KALSHI-ACCESS-KEY header")
	}
	if req.Header.Get("KALSHI-ACCESS-TIMESTAMP") != "1700000000000" {
		t.Error("missing KALSHI-ACCESS-TIMESTAMP header")
	}
	sig := req.Header.Get("KALSHI-ACCESS-SIGNATURE")
	if sig == "" {
		t.Error("missing KALSHI-ACCESS-SIGNATURE header")
	}
	// Base64-encoded RSA signature should be non-empty
	if len(sig) < 40 {
		t.Errorf("signature seems too short: %d chars", len(sig))
	}
}

func TestParseRSAPrivateKey_PKCS1(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemData := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	parsed, err := parseRSAPrivateKey(pemData)
	if err != nil {
		t.Fatalf("parse PKCS1: %v", err)
	}
	if parsed.N.Cmp(key.N) != 0 {
		t.Error("parsed key does not match original")
	}
}

func TestParseRSAPrivateKey_PKCS8(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemData := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: pkcs8Bytes,
	})

	parsed, err := parseRSAPrivateKey(pemData)
	if err != nil {
		t.Fatalf("parse PKCS8: %v", err)
	}
	if parsed.N.Cmp(key.N) != 0 {
		t.Error("parsed key does not match original")
	}
}

func TestNewClient_FromFile(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pemData := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "kalshi.key")
	os.WriteFile(keyPath, pemData, 0600)

	c, err := NewClient(Config{
		BaseURL:        "https://demo-api.kalshi.co/trade-api/v2",
		KeyID:          "test",
		PrivateKeyPath: keyPath,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.privateKey == nil {
		t.Error("private key not loaded")
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

	c := NewTestClient(srv.URL)

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

	c := NewTestClient(srv.URL)

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

	c := NewTestClient(srv.URL)

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

	c := NewTestClient(srv.URL)

	start := time.Now()
	for i := 0; i < 5; i++ {
		c.GetMarkets(context.Background(), "", 1)
	}
	elapsed := time.Since(start)

	// With 10 req/sec and burst=1, 5 requests should take at least ~400ms
	if elapsed < 350*time.Millisecond {
		t.Errorf("5 requests completed in %v, expected >= 350ms (rate limiter not working)", elapsed)
	}
	if requests != 5 {
		t.Errorf("expected 5 requests, got %d", requests)
	}
}

func TestGetMarkets_Integration(t *testing.T) {
	keyID := os.Getenv("KALSHI_API_KEY_ID")
	keyPath := os.Getenv("KALSHI_PRIVATE_KEY_PATH")
	if keyID == "" || keyPath == "" {
		t.Skip("KALSHI_API_KEY_ID and KALSHI_PRIVATE_KEY_PATH not set, skipping integration test")
	}

	baseURL := os.Getenv("KALSHI_API_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.elections.kalshi.com/trade-api/v2"
	}

	c, err := NewClient(Config{
		BaseURL:        baseURL,
		KeyID:          keyID,
		PrivateKeyPath: keyPath,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

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
