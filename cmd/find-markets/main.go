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

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// Skip all parlays (KXMVE*), collect single-event markets, check orderbooks
	fmt.Println("Scanning for single-event markets with liquidity...")
	fmt.Println("(Skipping ~10K parlay markets, this takes a minute)\n")

	cursor := ""
	prefixes := make(map[string]int)
	found := 0
	checked := 0
	pages := 0

	for pages < 300 && found < 15 {
		resp, err := c.GetMarkets(ctx, cursor, 200)
		if err != nil {
			fmt.Fprintf(os.Stderr, "page %d: %v\n", pages, err)
			break
		}
		pages++

		for _, m := range resp.Markets {
			prefix := strings.SplitN(m.Ticker, "-", 3)[0]
			prefixes[prefix]++

			// Skip parlays
			if strings.HasPrefix(m.Ticker, "KXMVE") || strings.HasPrefix(m.Ticker, "KXMUC") {
				continue
			}
			if m.Status != "active" {
				continue
			}

			checked++
			ob, err := c.GetOrderbook(ctx, m.Ticker)
			if err != nil {
				continue
			}
			bidPrice, bidDepth := kalshi.BestBid(ob.Yes)
			askPrice, askDepth := kalshi.BestAsk(ob.No)

			if bidPrice > 0 || askPrice > 0 {
				fmt.Printf("%-55s\n  ticker:    %s\n  yes_bid:   %d¢ (depth %d)\n  no_ask:    %d¢ (depth %d)\n\n",
					m.Title, m.Ticker, bidPrice, bidDepth, askPrice, askDepth)
				found++
			}
		}

		cursor = resp.Cursor
		if cursor == "" {
			break
		}

		if pages%20 == 0 {
			fmt.Printf("  ... page %d, checked %d orderbooks, found %d with liquidity\n", pages, checked, found)
		}
	}

	fmt.Printf("\n=== Summary ===\n")
	fmt.Printf("Pages scanned: %d\n", pages)
	fmt.Printf("Orderbooks checked: %d\n", checked)
	fmt.Printf("Markets with liquidity: %d\n\n", found)

	fmt.Println("Unique prefixes seen:")
	for prefix, count := range prefixes {
		if !strings.HasPrefix(prefix, "KXMVE") {
			fmt.Printf("  %-45s %d\n", prefix, count)
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
