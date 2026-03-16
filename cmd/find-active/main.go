package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"autonomy-platform/pkg/kalshi"
)

func main() {
	c, err := kalshi.NewClient(kalshi.Config{
		BaseURL:        envOr("KALSHI_API_BASE_URL", "https://api.elections.kalshi.com/trade-api/v2"),
		KeyID:          os.Getenv("KALSHI_API_KEY_ID"),
		PrivateKeyPath: os.Getenv("KALSHI_PRIVATE_KEY_PATH"),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "client: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	fmt.Println("Finding active markets (any orderbook activity)...")

	cursor := ""
	found := 0
	pages := 0

	for pages < 100 && found < 10 {
		resp, err := c.GetMarkets(ctx, cursor, 200)
		if err != nil {
			fmt.Fprintf(os.Stderr, "page %d: %v\n", pages, err)
			break
		}
		pages++

		for _, m := range resp.Markets {
			// Skip parlays
			if strings.HasPrefix(m.Ticker, "KXMVE") || strings.HasPrefix(m.Ticker, "KXMUC") {
				continue
			}
			if m.Status != "active" {
				continue
			}

			ob, err := c.GetOrderbook(ctx, m.Ticker)
			if err != nil {
				continue
			}

			// Check if ANY side has orders (less strict than find-markets)
			yesHasOrders := len(ob.Yes) > 0
			noHasOrders := len(ob.No) > 0

			if yesHasOrders || noHasOrders {
				bidPrice, bidDepth := kalshi.BestBid(ob.Yes)
				askPrice, askDepth := kalshi.BestAsk(ob.No)
				fmt.Printf("FOUND: %s\n  title:  %s\n  ticker: %s\n  yes_bid: %d¢ (depth %d), no_ask: %d¢ (depth %d)\n  yes_levels: %d, no_levels: %d\n\n",
					m.Ticker, m.Title, m.Ticker,
					bidPrice, bidDepth, askPrice, askDepth,
					len(ob.Yes), len(ob.No))
				found++
			}
		}

		cursor = resp.Cursor
		if cursor == "" {
			break
		}

		if pages%10 == 0 {
			fmt.Printf("  ... page %d, found %d\n", pages, found)
		}
	}

	fmt.Printf("\nTotal found: %d\n", found)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
