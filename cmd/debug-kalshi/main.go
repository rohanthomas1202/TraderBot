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

	fmt.Println("Finding active Kalshi markets with prices...")
	cursor := ""
	found := 0

	for page := 0; page < 100 && found < 15; page++ {
		resp, err := c.GetMarkets(ctx, cursor, 200)
		if err != nil {
			fmt.Fprintf(os.Stderr, "page %d: %v\n", page, err)
			break
		}

		for _, m := range resp.Markets {
			if m.Status != "active" {
				continue
			}
			if strings.HasPrefix(m.Ticker, "KXMVE") || strings.HasPrefix(m.Ticker, "KXMUC") {
				continue
			}

			yesBid := m.YesBidCents()
			yesAsk := m.YesAskCents()
			lastP := m.LastPriceCents()

			if yesBid > 0 || yesAsk > 0 || lastP > 0 {
				fmt.Printf("%-50s  bid=%2d¢  ask=%2d¢  last=%2d¢  oi=%s  vol24h=%s\n",
					m.Ticker, yesBid, yesAsk, lastP, m.OpenInterestFP, m.Volume24hFP)
				found++
			}
		}

		cursor = resp.Cursor
		if cursor == "" {
			break
		}
		if page%20 == 0 && page > 0 {
			fmt.Printf("  ... page %d, found %d\n", page, found)
		}
	}

	fmt.Printf("\nTotal: %d markets with prices\n", found)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
