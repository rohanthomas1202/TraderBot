package execution

import (
	"context"
	"fmt"
	"time"

	"autonomy-platform/gen/commonpb"
	"autonomy-platform/internal/convert"
	"autonomy-platform/gen/executionpb"
	"autonomy-platform/gen/riskpb"
	"autonomy-platform/internal/models"
	"autonomy-platform/services/risk"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GRPCServer adapts the execution Engine to the generated gRPC service interface.
type GRPCServer struct {
	executionpb.UnimplementedExecutionEngineServer
	engine *Engine
	db     *pgxpool.Pool
}

func NewGRPCServer(engine *Engine, db *pgxpool.Pool) *GRPCServer {
	return &GRPCServer{engine: engine, db: db}
}

func (s *GRPCServer) SubmitOrder(ctx context.Context, req *executionpb.SubmitOrderRequest) (*executionpb.SubmitOrderResponse, error) {
	if req.GetApproval() == nil {
		return nil, status.Error(codes.InvalidArgument, "signed approval is required")
	}

	approval := approvalFromProto(req.GetApproval())
	rec, err := s.engine.SubmitOrder(ctx, approval)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "submit order: %v", err)
	}

	return &executionpb.SubmitOrderResponse{
		Order: convert.OrderRecordToProto(rec),
	}, nil
}

func (s *GRPCServer) CancelOrder(ctx context.Context, req *executionpb.CancelOrderRequest) (*executionpb.CancelOrderResponse, error) {
	err := s.engine.CancelOrder(ctx, req.GetInternalOrderId(), req.GetReason(), req.GetCancelledBy())
	if err != nil {
		return &executionpb.CancelOrderResponse{Success: false, Detail: err.Error()}, nil
	}
	return &executionpb.CancelOrderResponse{Success: true}, nil
}

func (s *GRPCServer) CancelAll(ctx context.Context, req *executionpb.CancelAllRequest) (*executionpb.CancelAllResponse, error) {
	cancelled, err := s.engine.CancelAll(ctx, req.GetReason(), req.GetCancelledBy())
	resp := &executionpb.CancelAllResponse{CancelledCount: int32(cancelled)}
	if err != nil {
		resp.FailedCount = 1
	}
	return resp, nil
}

func (s *GRPCServer) GetOrders(ctx context.Context, req *executionpb.GetOrdersRequest) (*executionpb.GetOrdersResponse, error) {
	limit := req.GetLimit()
	if limit <= 0 {
		limit = 100
	}

	query := `SELECT internal_order_id, trace_id, strategy_id, venue, market_id, side,
	                 quantity, price_micros, notional_micros, exchange_order_id, status,
	                 filled_quantity, avg_fill_price_micros, credential_id,
	                 proposed_at, submitted_at
	          FROM execution.orders WHERE 1=1`
	args := []interface{}{}
	argIdx := 1

	if len(req.GetStatuses()) > 0 {
		query += fmt.Sprintf(" AND status = ANY($%d)", argIdx)
		args = append(args, req.GetStatuses())
		argIdx++
	}
	if req.GetStrategyId() != "" {
		query += fmt.Sprintf(" AND strategy_id = $%d", argIdx)
		args = append(args, req.GetStrategyId())
		argIdx++
	}
	query += fmt.Sprintf(" ORDER BY proposed_at DESC LIMIT $%d", argIdx)
	args = append(args, limit)

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query orders: %v", err)
	}
	defer rows.Close()

	var orders []*executionpb.OrderRecord
	for rows.Next() {
		var (
			internalID, traceID, strategyID, venue, marketID, sideStr string
			exchangeOrderID, statusStr, credentialID                  string
			quantity, filledQty                                       int32
			priceMicros, notionalMicros, avgFillPrice                 int64
			proposedAt                                                time.Time
			submittedAt                                               *time.Time
		)
		if err := rows.Scan(
			&internalID, &traceID, &strategyID, &venue, &marketID, &sideStr,
			&quantity, &priceMicros, &notionalMicros, &exchangeOrderID, &statusStr,
			&filledQty, &avgFillPrice, &credentialID,
			&proposedAt, &submittedAt,
		); err != nil {
			return nil, status.Errorf(codes.Internal, "scan order: %v", err)
		}

		rec := &executionpb.OrderRecord{
			TraceId:            traceID,
			InternalOrderId:    internalID,
			ExchangeOrderId:    exchangeOrderID,
			Status:             convert.OrderStatusToProto(models.OrderStatus(statusStr)),
			FilledQuantity:     filledQty,
			AvgFillPriceMicros: avgFillPrice,
			CredentialId:       credentialID,
			Proposed: &riskpb.ProposedOrder{
				TraceId:        traceID,
				StrategyId:     strategyID,
				Market:         &commonpb.Market{Venue: convert.VenueToProto(venue), MarketId: marketID},
				Side:           convert.SideToProto(models.Side(sideStr)),
				OrderType:      commonpb.OrderType_ORDER_TYPE_LIMIT,
				Quantity:       quantity,
				PriceMicros:    priceMicros,
				NotionalMicros: notionalMicros,
				ProposedAt:     timestamppb.New(proposedAt),
			},
		}
		if submittedAt != nil {
			rec.SubmittedAt = timestamppb.New(*submittedAt)
		}
		orders = append(orders, rec)
	}

	return &executionpb.GetOrdersResponse{Orders: orders}, nil
}

func (s *GRPCServer) GetOrderSummary(ctx context.Context, req *executionpb.GetOrderSummaryRequest) (*executionpb.GetOrderSummaryResponse, error) {
	today := time.Now().UTC().Format("2006-01-02")

	resp := &executionpb.GetOrderSummaryResponse{}
	err := s.db.QueryRow(ctx,
		`SELECT
			COALESCE(SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'open' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'partially_filled' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'filled' AND proposed_at::date = $1 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'cancelled' AND proposed_at::date = $1 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'rejected' AND proposed_at::date = $1 THEN 1 ELSE 0 END), 0)
		 FROM execution.orders`,
		today,
	).Scan(&resp.Pending, &resp.Open, &resp.PartiallyFilled, &resp.FilledToday, &resp.CancelledToday, &resp.RejectedToday)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query order summary: %v", err)
	}

	return resp, nil
}

// ─── Conversions (package-local to avoid import cycles) ───

func approvalFromProto(p *riskpb.SignedApproval) *risk.Approval {
	checks := make([]risk.CheckResultDetail, len(p.GetChecks()))
	for i, c := range p.GetChecks() {
		checks[i] = risk.CheckResultDetail{
			Name:   c.GetCheckName(),
			Passed: c.GetResult() == riskpb.CheckResult_CHECK_PASS,
			Detail: c.GetDetail(),
		}
	}

	decision := risk.DecisionDenied
	switch p.GetDecision() {
	case riskpb.ApprovalDecision_DECISION_APPROVED:
		decision = risk.DecisionApproved
	case riskpb.ApprovalDecision_DECISION_ESCALATED:
		decision = risk.DecisionEscalated
	}

	var decidedAt time.Time
	if p.GetDecidedAt() != nil {
		decidedAt = p.GetDecidedAt().AsTime()
	}

	return &risk.Approval{
		TraceID:          p.GetTraceId(),
		Order:            convert.ProposedOrderFromProto(p.GetOrder()),
		Decision:         decision,
		Checks:           checks,
		PolicyConfigHash: p.GetPolicyConfigHash(),
		DecidedAt:        decidedAt,
		HMACSignature:    p.GetHmacSignature(),
	}
}
