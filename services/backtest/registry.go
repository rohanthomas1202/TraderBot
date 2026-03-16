package backtest

import (
	"fmt"

	"autonomy-platform/services/strategy"
)

// Registry maps strategy names to their SignalFunc constructors.
var Registry = map[string]func() strategy.SignalFunc{
	"simple-momentum": strategy.SimpleMomentum,
}

// GetStrategy returns a SignalFunc for the given strategy name.
func GetStrategy(name string) (strategy.SignalFunc, error) {
	ctor, ok := Registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown strategy: %s (available: %v)", name, strategyNames())
	}
	return ctor(), nil
}

func strategyNames() []string {
	names := make([]string, 0, len(Registry))
	for k := range Registry {
		names = append(names, k)
	}
	return names
}
