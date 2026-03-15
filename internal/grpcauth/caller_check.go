package grpcauth

import (
	"context"
	"slices"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const CallerHeader = "x-caller-service"

// AllowedCallers maps full gRPC method name to a list of allowed caller service names.
// Use "*" to allow any caller.
type AllowedCallers map[string][]string

// UnaryCallerInterceptor returns a server-side interceptor that validates the
// x-caller-service metadata against the AllowedCallers map.
// Methods not in the map are open to all callers.
func UnaryCallerInterceptor(ac AllowedCallers) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		allowed, exists := ac[info.FullMethod]
		if !exists {
			return handler(ctx, req)
		}

		if slices.Contains(allowed, "*") {
			return handler(ctx, req)
		}

		caller := callerFromContext(ctx)
		if caller == "" {
			return nil, status.Errorf(codes.PermissionDenied, "missing %s metadata", CallerHeader)
		}

		if !slices.Contains(allowed, caller) {
			return nil, status.Errorf(codes.PermissionDenied, "caller %q not allowed for %s", caller, info.FullMethod)
		}

		return handler(ctx, req)
	}
}

// CallerIdentityInterceptor returns a client-side interceptor that injects
// the x-caller-service header into all outgoing requests.
func CallerIdentityInterceptor(serviceName string) grpc.DialOption {
	return grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx = metadata.AppendToOutgoingContext(ctx, CallerHeader, serviceName)
		return invoker(ctx, method, req, reply, cc, opts...)
	})
}

func callerFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(CallerHeader)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}
