package models

import "time"

type MarketData struct {
	Venue         string
	MarketID      string
	DisplayName   string
	BidPriceMicros int64
	AskPriceMicros int64
	LastPriceMicros int64
	BidDepth      int32
	AskDepth      int32
	Volume24h     float64
	UpdatedAt     time.Time
}

func (m *MarketData) SpreadMicros() int64 {
	return m.AskPriceMicros - m.BidPriceMicros
}

func (m *MarketData) SpreadBps() int64 {
	mid := (m.BidPriceMicros + m.AskPriceMicros) / 2
	if mid == 0 {
		return 0
	}
	return m.SpreadMicros() * 10000 / mid
}

func (m *MarketData) MidPriceMicros() int64 {
	return (m.BidPriceMicros + m.AskPriceMicros) / 2
}

func (m *MarketData) AgeSeconds(now time.Time) float64 {
	return now.Sub(m.UpdatedAt).Seconds()
}
