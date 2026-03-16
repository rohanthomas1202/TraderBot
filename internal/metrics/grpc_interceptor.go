package metrics

import (
	"context"
	"path"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// UnaryServerMetricsInterceptor returns a gRPC server interceptor that records
// request duration and status code for every unary RPC.
func UnaryServerMetricsInterceptor(serviceName string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		elapsed := time.Since(start).Seconds()

		code := status.Code(err).String()
		method := path.Base(info.FullMethod)

		GRPCRequestDuration.WithLabelValues(serviceName, method, code).Observe(elapsed)

		return resp, err
	}
}
