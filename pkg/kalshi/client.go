package kalshi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/time/rate"
)

// Config holds Kalshi API connection settings.
type Config struct {
	BaseURL   string // e.g. "https://demo-api.kalshi.co/trade-api/v2"
	KeyID     string // KALSHI_API_KEY_ID
	KeySecret string // KALSHI_API_KEY_SECRET (hex-encoded)
}

// Client is a read-only Kalshi REST API client.
// It enforces rate limiting (10 req/sec) and provides only GET endpoints.
type Client struct {
	httpClient *http.Client
	config     Config
	limiter    *rate.Limiter
	logger     *slog.Logger
}

// NewClient creates a new Kalshi API client with rate limiting.
func NewClient(cfg Config) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		config:     cfg,
		limiter:    rate.NewLimiter(10, 1), // 10 req/sec, burst 1
		logger:     slog.Default().With("component", "kalshi-client"),
	}
}

// signRequest adds Kalshi authentication headers to the request.
// Signature = HMAC-SHA256(timestamp + method + path) using the key secret.
func (c *Client) signRequest(req *http.Request, timestamp string) error {
	message := timestamp + req.Method + req.URL.Path
	secret, err := hex.DecodeString(c.config.KeySecret)
	if err != nil {
		return fmt.Errorf("decode key secret: %w", err)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(message))
	signature := hex.EncodeToString(mac.Sum(nil))

	req.Header.Set("KALSHI-ACCESS-KEY", c.config.KeyID)
	req.Header.Set("KALSHI-ACCESS-TIMESTAMP", timestamp)
	req.Header.Set("KALSHI-ACCESS-SIGNATURE", signature)
	return nil
}

// doGet performs an authenticated, rate-limited GET request.
func (c *Client) doGet(ctx context.Context, path string, result interface{}) error {
	if err := c.limiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limiter: %w", err)
	}

	url := c.config.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	if err := c.signRequest(req, timestamp); err != nil {
		return fmt.Errorf("sign request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("kalshi API %s: status %d: %s", path, resp.StatusCode, string(body))
	}

	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// Market represents a Kalshi market.
type Market struct {
	Ticker          string `json:"ticker"`
	Title           string `json:"title"`
	Subtitle        string `json:"subtitle"`
	Status          string `json:"status"` // "open", "closed", "settled"
	YesBid          int    `json:"yes_bid"`
	YesAsk          int    `json:"yes_ask"`
	NoBid           int    `json:"no_bid"`
	NoAsk           int    `json:"no_ask"`
	LastPrice       int    `json:"last_price"`
	Volume          int    `json:"volume"`
	Volume24h       int    `json:"volume_24h"`
	OpenInterest    int    `json:"open_interest"`
	PreviousYesBid  int    `json:"previous_yes_bid"`
	PreviousYesAsk  int    `json:"previous_yes_ask"`
	PreviousPrice   int    `json:"previous_price"`
}

// Orderbook represents a Kalshi market orderbook.
type Orderbook struct {
	Yes [][]int `json:"yes"` // [[price_cents, quantity], ...]
	No  [][]int `json:"no"`  // [[price_cents, quantity], ...]
}

// OrderbookResponse wraps the orderbook API response.
type OrderbookResponse struct {
	Orderbook Orderbook `json:"orderbook"`
}

// MarketsResponse is the paginated response from GET /markets.
type MarketsResponse struct {
	Markets []Market `json:"markets"`
	Cursor  string   `json:"cursor"`
}

// MarketResponse wraps a single market response.
type MarketResponse struct {
	Market Market `json:"market"`
}

// GetMarkets lists markets with optional pagination.
func (c *Client) GetMarkets(ctx context.Context, cursor string, limit int) (*MarketsResponse, error) {
	path := "/markets"
	sep := '?'

	if limit > 0 {
		path += fmt.Sprintf("%climit=%d", sep, limit)
		sep = '&'
	}
	if cursor != "" {
		path += fmt.Sprintf("%ccursor=%s", sep, cursor)
	}

	var resp MarketsResponse
	if err := c.doGet(ctx, path, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetMarket fetches a single market by ticker.
func (c *Client) GetMarket(ctx context.Context, ticker string) (*Market, error) {
	var resp MarketResponse
	if err := c.doGet(ctx, "/markets/"+ticker, &resp); err != nil {
		return nil, err
	}
	return &resp.Market, nil
}

// GetOrderbook fetches the orderbook for a market.
func (c *Client) GetOrderbook(ctx context.Context, ticker string) (*Orderbook, error) {
	var resp OrderbookResponse
	if err := c.doGet(ctx, "/markets/"+ticker+"/orderbook", &resp); err != nil {
		return nil, err
	}
	return &resp.Orderbook, nil
}

// BestBid returns the best (highest) bid price and total depth from an orderbook side.
// Returns (0, 0) if the side is empty.
func BestBid(levels [][]int) (priceCents int, depth int32) {
	if len(levels) == 0 {
		return 0, 0
	}
	best := 0
	var totalDepth int32
	for _, level := range levels {
		if len(level) >= 2 {
			if level[0] > best {
				best = level[0]
			}
			totalDepth += int32(level[1])
		}
	}
	return best, totalDepth
}

// BestAsk returns the best (lowest) ask price and total depth from an orderbook side.
// Returns (0, 0) if the side is empty.
func BestAsk(levels [][]int) (priceCents int, depth int32) {
	if len(levels) == 0 {
		return 0, 0
	}
	best := 101 // prices are 0-100, so 101 is above max
	var totalDepth int32
	for _, level := range levels {
		if len(level) >= 2 {
			if level[0] < best {
				best = level[0]
			}
			totalDepth += int32(level[1])
		}
	}
	if best == 101 {
		return 0, 0
	}
	return best, totalDepth
}

// CentsToMicros converts Kalshi cents (0-100) to microdollars.
// 65 cents = $0.65 = 650,000 micros.
func CentsToMicros(cents int) int64 {
	return int64(cents) * 10_000
}
