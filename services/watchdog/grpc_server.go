package watchdog

import (
	"context"
	"time"

	"autonomy-platform/internal/convert"
	"autonomy-platform/gen/watchdogpb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GRPCServer adapts the watchdog subsystems to the generated gRPC service interface.
type GRPCServer struct {
	watchdogpb.UnimplementedWatchdogServer
	killMgr *KillSwitchManager
	dms     *DeadMansSwitch
}

func NewGRPCServer(killMgr *KillSwitchManager, dms *DeadMansSwitch) *GRPCServer {
	return &GRPCServer{killMgr: killMgr, dms: dms}
}

func (s *GRPCServer) TriggerKillSwitch(ctx context.Context, req *watchdogpb.KillSwitchRequest) (*watchdogpb.KillSwitchResponse, error) {
	level := killSwitchLevelFromProto(req.GetLevel())
	if level == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid kill switch level")
	}

	if err := s.killMgr.Trigger(ctx, level, req.GetScope(), req.GetReason(), req.GetTriggeredBy()); err != nil {
		return &watchdogpb.KillSwitchResponse{Executed: false, Detail: err.Error()}, nil
	}
	return &watchdogpb.KillSwitchResponse{Executed: true}, nil
}

func (s *GRPCServer) GetSystemStatus(ctx context.Context, req *watchdogpb.SystemStatusRequest) (*watchdogpb.SystemStatus, error) {
	mode := s.killMgr.GetCurrentMode()
	halts := s.killMgr.GetActiveHalts()

	protoHalts := make([]*watchdogpb.ActiveHalt, len(halts))
	for i := range halts {
		protoHalts[i] = activeHaltToProto(&halts[i])
	}

	healthMap := s.dms.GetServiceHealth()
	protoHealth := make(map[string]*watchdogpb.ServiceHealth, len(healthMap))
	for k, v := range healthMap {
		protoHealth[k] = serviceHealthToProto(&v)
	}

	return &watchdogpb.SystemStatus{
		CurrentMode:   mode,
		ActiveHalts:   protoHalts,
		ServiceHealth: protoHealth,
		AsOf:          timestamppb.New(time.Now().UTC()),
	}, nil
}

func (s *GRPCServer) AcknowledgeHalt(ctx context.Context, req *watchdogpb.AcknowledgeHaltRequest) (*watchdogpb.AcknowledgeHaltResponse, error) {
	if err := s.killMgr.Acknowledge(ctx, req.GetScope(), req.GetAcknowledgedBy(), req.GetRootCause()); err != nil {
		return &watchdogpb.AcknowledgeHaltResponse{Success: false, Detail: err.Error()}, nil
	}
	return &watchdogpb.AcknowledgeHaltResponse{Success: true}, nil
}

func (s *GRPCServer) ResumeTrading(ctx context.Context, req *watchdogpb.ResumeTradingRequest) (*watchdogpb.ResumeTradingResponse, error) {
	if err := s.killMgr.Resume(ctx, req.GetScope(), req.GetResumedBy()); err != nil {
		return &watchdogpb.ResumeTradingResponse{Success: false, Detail: err.Error()}, nil
	}
	return &watchdogpb.ResumeTradingResponse{Success: true}, nil
}

func (s *GRPCServer) Heartbeat(ctx context.Context, req *watchdogpb.HeartbeatRequest) (*watchdogpb.HeartbeatResponse, error) {
	s.dms.RecordHeartbeat(ctx, req.GetServiceName(), req.GetStatus(), req.GetDetail())
	return &watchdogpb.HeartbeatResponse{
		Acknowledged: true,
		SystemMode:   s.killMgr.GetCurrentMode(),
	}, nil
}

// ─── Conversions (package-local to avoid import cycles) ───

func killSwitchLevelFromProto(l watchdogpb.KillSwitchLevel) KillSwitchLevel {
	level := convert.KillSwitchLevelFromProto(l)
	return KillSwitchLevel(level)
}

func activeHaltToProto(h *ActiveHalt) *watchdogpb.ActiveHalt {
	ph := &watchdogpb.ActiveHalt{
		Level:          convert.KillSwitchLevelToProto(string(h.Level)),
		Scope:          h.Scope,
		Reason:         h.Reason,
		TriggeredBy:    h.TriggeredBy,
		TriggeredAt:    timestamppb.New(h.TriggeredAt),
		Acknowledged:   h.Acknowledged,
		AcknowledgedBy: h.AckedBy,
	}
	if h.AckedAt != nil {
		ph.AcknowledgedAt = timestamppb.New(*h.AckedAt)
	}
	return ph
}

func serviceHealthToProto(h *ServiceHealth) *watchdogpb.ServiceHealth {
	return &watchdogpb.ServiceHealth{
		ServiceName:   h.ServiceName,
		Healthy:       h.Healthy,
		LastHeartbeat: timestamppb.New(h.LastHeartbeat),
	}
}
