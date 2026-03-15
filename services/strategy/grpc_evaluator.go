package strategy

import (
	"context"
	"log/slog"

	"autonomy-platform/internal/convert"
	"autonomy-platform/gen/executionpb"
	"autonomy-platform/gen/riskpb"
	"autonomy-platform/internal/models"
)

// GRPCEvaluator implements OrderEvaluator by calling the risk engine and
// execution engine over gRPC. On approval, it submits the order to execution.
type GRPCEvaluator struct {
	riskClient riskpb.RiskEngineClient
	execClient executionpb.ExecutionEngineClient
	logger     *slog.Logger
}

func NewGRPCEvaluator(riskClient riskpb.RiskEngineClient, execClient executionpb.ExecutionEngineClient) *GRPCEvaluator {
	return &GRPCEvaluator{
		riskClient: riskClient,
		execClient: execClient,
		logger:     slog.Default().With("component", "grpc-evaluator"),
	}
}

func (g *GRPCEvaluator) EvaluateOrder(ctx context.Context, order *models.ProposedOrder) (bool, error) {
	// Step 1: Call risk engine for evaluation
	resp, err := g.riskClient.EvaluateOrder(ctx, &riskpb.EvaluateOrderRequest{
		Order: convert.ProposedOrderToProto(order),
	})
	if err != nil {
		return false, err
	}

	approval := resp.GetApproval()
	if approval == nil {
		return false, nil
	}

	if approval.GetDecision() != riskpb.ApprovalDecision_DECISION_APPROVED {
		g.logger.Debug("order denied by risk engine",
			"trace_id", order.TraceID,
			"decision", approval.GetDecision().String(),
		)
		return false, nil
	}

	// Step 2: Submit approved order to execution engine
	submitResp, err := g.execClient.SubmitOrder(ctx, &executionpb.SubmitOrderRequest{
		Approval: approval,
	})
	if err != nil {
		g.logger.Error("failed to submit order to execution engine",
			"trace_id", order.TraceID,
			"error", err,
		)
		return false, err
	}

	g.logger.Info("order submitted",
		"trace_id", order.TraceID,
		"internal_order_id", submitResp.GetOrder().GetInternalOrderId(),
		"status", submitResp.GetOrder().GetStatus().String(),
	)

	return true, nil
}
