package watchdog

import (
	"context"
	"log/slog"

	"autonomy-platform/gen/executionpb"
)

// GRPCExecControl implements ExecutionControl by calling the execution engine over gRPC.
type GRPCExecControl struct {
	client executionpb.ExecutionEngineClient
	logger *slog.Logger
}

func NewGRPCExecControl(client executionpb.ExecutionEngineClient) *GRPCExecControl {
	return &GRPCExecControl{
		client: client,
		logger: slog.Default().With("component", "grpc-exec-control"),
	}
}

func (g *GRPCExecControl) CancelAll(ctx context.Context, reason, cancelledBy string) (int, error) {
	resp, err := g.client.CancelAll(ctx, &executionpb.CancelAllRequest{
		Scope:       "global",
		Reason:      reason,
		CancelledBy: cancelledBy,
	})
	if err != nil {
		return 0, err
	}
	return int(resp.GetCancelledCount()), nil
}

func (g *GRPCExecControl) SetSystemMode(mode string) {
	// Mode propagated via heartbeat responses and NATS kill switch events.
	// The execution engine reads system_mode from every heartbeat response.
	g.logger.Info("mode change requested (propagated via heartbeat)", "mode", mode)
}

// GRPCRiskControl implements RiskControl. Mode propagation happens via
// the risk engine's NATS subscription to kill switch events.
type GRPCRiskControl struct {
	logger *slog.Logger
}

func NewGRPCRiskControl() *GRPCRiskControl {
	return &GRPCRiskControl{
		logger: slog.Default().With("component", "grpc-risk-control"),
	}
}

func (g *GRPCRiskControl) SetSystemMode(mode string) {
	// Risk engine subscribes to NATS kill switch events and updates its own mode.
	g.logger.Info("mode change requested (propagated via NATS)", "mode", mode)
}
