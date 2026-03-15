package risk

import (
	"context"
	"log/slog"
	"time"

	"autonomy-platform/gen/commonpb"
	"autonomy-platform/gen/riskpb"
	"autonomy-platform/internal/convert"
	"autonomy-platform/internal/logging"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GRPCServer adapts the risk Engine to the generated gRPC service interface.
type GRPCServer struct {
	riskpb.UnimplementedRiskEngineServer
	engine *Engine
}

func NewGRPCServer(engine *Engine) *GRPCServer {
	return &GRPCServer{engine: engine}
}

func (s *GRPCServer) EvaluateOrder(ctx context.Context, req *riskpb.EvaluateOrderRequest) (*riskpb.EvaluateOrderResponse, error) {
	if req.GetOrder() == nil {
		return nil, status.Error(codes.InvalidArgument, "order is required")
	}

	if tid := req.GetOrder().GetTraceId(); tid != "" {
		ctx = logging.WithTraceID(ctx, tid)
	}
	logger := logging.LoggerWithTrace(ctx, slog.Default())

	order := convert.ProposedOrderFromProto(req.GetOrder())
	approval, err := s.engine.EvaluateOrder(ctx, order)
	if err != nil {
		logger.Error("order evaluation failed", "error", err)
		return nil, status.Errorf(codes.Internal, "evaluate order: %v", err)
	}

	logger.Info("order evaluated", "decision", approval.Decision, "strategy", order.StrategyID)

	return &riskpb.EvaluateOrderResponse{
		Approval: approvalToProto(approval),
	}, nil
}

func (s *GRPCServer) GetRiskState(ctx context.Context, req *riskpb.GetRiskStateRequest) (*riskpb.RiskState, error) {
	state := s.engine.GetState()
	return riskStateToProto(state), nil
}

func (s *GRPCServer) ReportFill(ctx context.Context, req *riskpb.ReportFillRequest) (*riskpb.ReportFillResponse, error) {
	if tid := req.GetTraceId(); tid != "" {
		ctx = logging.WithTraceID(ctx, tid)
	}
	logger := logging.LoggerWithTrace(ctx, slog.Default())

	fill := fillReportFromProto(req)
	if err := s.engine.ReportFill(ctx, fill); err != nil {
		logger.Error("fill report failed", "error", err)
		return nil, status.Errorf(codes.Internal, "report fill: %v", err)
	}
	return &riskpb.ReportFillResponse{Acknowledged: true}, nil
}

func (s *GRPCServer) UpdateLimit(ctx context.Context, req *riskpb.UpdateLimitRequest) (*riskpb.UpdateLimitResponse, error) {
	return nil, status.Error(codes.Unimplemented, "UpdateLimit not implemented in this phase")
}

// ─── Conversions (package-local to avoid import cycles) ───

func approvalToProto(a *Approval) *riskpb.SignedApproval {
	checks := make([]*riskpb.PolicyCheckDetail, len(a.Checks))
	for i, c := range a.Checks {
		result := riskpb.CheckResult_CHECK_PASS
		if !c.Passed {
			result = riskpb.CheckResult_CHECK_FAIL
		}
		checks[i] = &riskpb.PolicyCheckDetail{
			CheckName: c.Name,
			Result:    result,
			Detail:    c.Detail,
		}
	}

	decision := riskpb.ApprovalDecision_DECISION_DENIED
	switch a.Decision {
	case DecisionApproved:
		decision = riskpb.ApprovalDecision_DECISION_APPROVED
	case DecisionEscalated:
		decision = riskpb.ApprovalDecision_DECISION_ESCALATED
	}

	return &riskpb.SignedApproval{
		TraceId:          a.TraceID,
		Order:            convert.ProposedOrderToProto(a.Order),
		Decision:         decision,
		Checks:           checks,
		PolicyConfigHash: a.PolicyConfigHash,
		DecidedAt:        timestamppb.New(a.DecidedAt),
		HmacSignature:    a.HMACSignature,
	}
}

func approvalFromProto(p *riskpb.SignedApproval) *Approval {
	checks := make([]CheckResultDetail, len(p.GetChecks()))
	for i, c := range p.GetChecks() {
		checks[i] = CheckResultDetail{
			Name:   c.GetCheckName(),
			Passed: c.GetResult() == riskpb.CheckResult_CHECK_PASS,
			Detail: c.GetDetail(),
		}
	}

	decision := DecisionDenied
	switch p.GetDecision() {
	case riskpb.ApprovalDecision_DECISION_APPROVED:
		decision = DecisionApproved
	case riskpb.ApprovalDecision_DECISION_ESCALATED:
		decision = DecisionEscalated
	}

	var decidedAt time.Time
	if p.GetDecidedAt() != nil {
		decidedAt = p.GetDecidedAt().AsTime()
	}

	return &Approval{
		TraceID:          p.GetTraceId(),
		Order:            convert.ProposedOrderFromProto(p.GetOrder()),
		Decision:         decision,
		Checks:           checks,
		PolicyConfigHash: p.GetPolicyConfigHash(),
		DecidedAt:        decidedAt,
		HMACSignature:    p.GetHmacSignature(),
	}
}

func fillReportFromProto(p *riskpb.ReportFillRequest) *FillReport {
	var venue, marketID string
	if p.GetMarket() != nil {
		venue = convert.VenueFromProto(p.GetMarket().GetVenue())
		marketID = p.GetMarket().GetMarketId()
	}
	return &FillReport{
		TraceID:         p.GetTraceId(),
		InternalOrderID: p.GetInternalOrderId(),
		StrategyID:      p.GetStrategyId(),
		Venue:           venue,
		MarketID:        marketID,
		Side:            convert.SideFromProto(p.GetSide()),
		Quantity:        p.GetFilledQuantity(),
		PriceMicros:     p.GetFillPriceMicros(),
	}
}

func fillReportToProto(f *FillReport) *riskpb.ReportFillRequest {
	return &riskpb.ReportFillRequest{
		TraceId:         f.TraceID,
		InternalOrderId: f.InternalOrderID,
		StrategyId:      f.StrategyID,
		Market:          &commonpb.Market{Venue: convert.VenueToProto(f.Venue), MarketId: f.MarketID},
		Side:            convert.SideToProto(f.Side),
		FilledQuantity:  f.Quantity,
		FillPriceMicros: f.PriceMicros,
	}
}

func riskStateToProto(s *State) *riskpb.RiskState {
	strategies := make(map[string]*riskpb.StrategyRiskState, len(s.Strategies))
	for k, v := range s.Strategies {
		strategies[k] = &riskpb.StrategyRiskState{
			DailyPnl:          &commonpb.Money{AmountMicros: int64(v.DailyPnL)},
			Exposure:          &commonpb.Money{AmountMicros: int64(v.Exposure)},
			DailyOrderCount:   v.DailyOrderCount,
			ConsecutiveLosses: v.ConsecutiveLosses,
			Halted:            v.Halted,
			HaltReason:        v.HaltReason,
		}
	}

	venues := make(map[string]*riskpb.VenueRiskState, len(s.Venues))
	for k, v := range s.Venues {
		venues[k] = &riskpb.VenueRiskState{
			DailyPnl: &commonpb.Money{AmountMicros: int64(v.DailyPnL)},
			Exposure: &commonpb.Money{AmountMicros: int64(v.Exposure)},
			Halted:   v.Halted,
		}
	}

	markets := make(map[string]*riskpb.MarketRiskState, len(s.Markets))
	for k, v := range s.Markets {
		markets[k] = &riskpb.MarketRiskState{
			PositionContracts:      v.PositionContracts,
			PositionNotionalMicros: int64(v.PositionNotional),
		}
	}

	return &riskpb.RiskState{
		TotalExposure: &commonpb.Money{AmountMicros: int64(s.TotalExposure)},
		DailyPnl:      &commonpb.Money{AmountMicros: int64(s.DailyPnL)},
		PeakEquity:    &commonpb.Money{AmountMicros: int64(s.PeakEquity)},
		SystemMode:    s.SystemMode,
		Strategies:    strategies,
		Venues:        venues,
		Markets:       markets,
		AsOf:          timestamppb.Now(),
	}
}
