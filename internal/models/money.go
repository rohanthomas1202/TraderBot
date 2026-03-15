package models

import "fmt"

// Money represents USD in microdollars. 1 USD = 1,000,000 microdollars.
// Using int64 avoids all floating-point precision issues.
// Range: ±$9.2 trillion, which is sufficient.
type Money int64

const MicroPerDollar Money = 1_000_000

func USD(dollars float64) Money {
	return Money(dollars * float64(MicroPerDollar))
}

func (m Money) Dollars() float64 {
	return float64(m) / float64(MicroPerDollar)
}

func (m Money) String() string {
	return fmt.Sprintf("$%.2f", m.Dollars())
}

func (m Money) Abs() Money {
	if m < 0 {
		return -m
	}
	return m
}
