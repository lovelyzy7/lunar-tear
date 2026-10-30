//go:build diag

// Package interceptor — DIAGNOSTIC build only (go build -tags diag).
//
// TODO(diag): rollback = delete diag.go + diag_stub.go and remove the
// interceptor.DiagServerOptions() call in cmd/lunar-tear/grpc.go.
// The production build (no -tags diag) compiles diag_stub.go and behaves
// exactly as before.
package interceptor

import (
	"context"
	"log"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
)

// DiagServerOptions returns extra server options active only in -tags diag
// builds. UnknownServiceHandler is applied last so it REPLACES the stock
// interceptor.UnknownService with a richer logger; every unknown service or
// unknown method (grpc-go v1.80 server.go handleStream) lands here.
func DiagServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.ChainStreamInterceptor(DiagStream),
		grpc.StatsHandler(&DiagStats{}),
		grpc.UnknownServiceHandler(DiagUnknownService),
	}
}

func peerOf(ctx context.Context) string {
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		return p.Addr.String()
	}
	return "?"
}

// DiagUnknownService logs every RPC that is not registered on this server
// (unknown service OR unknown method on a known service) before returning
// Unimplemented — the class of calls the stock chain cannot see.
func DiagUnknownService(_ any, stream grpc.ServerStream) error {
	ctx := stream.Context()
	fullMethod, ok := grpc.MethodFromServerStream(stream)
	if !ok {
		fullMethod = "<unknown>"
	}
	md, _ := metadata.FromIncomingContext(ctx)
	log.Printf("[DIAG-UNKNOWN] peer=%s method=%s content-type=%v user-agent=%v",
		peerOf(ctx), fullMethod, md.Get("content-type"), md.Get("user-agent"))
	err := status.Errorf(codes.Unimplemented, "unknown service or method %s", fullMethod)
	log.Printf("[DIAG-UNKNOWN] peer=%s method=%s -> Unimplemented", peerOf(ctx), fullMethod)
	return err
}

// DiagStream closes the ChainUnaryInterceptor gap: streaming RPCs (none in the
// current protos, but any future/undocumented one) are logged here.
func DiagStream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	ctx := ss.Context()
	log.Printf("[DIAG-STREAM] >>> peer=%s method=%s", peerOf(ctx), info.FullMethod)
	start := time.Now()
	err := handler(srv, ss)
	log.Printf("[DIAG-STREAM] <<< peer=%s method=%s dur=%s err=%v", peerOf(ctx), info.FullMethod, time.Since(start).Round(time.Millisecond), err)
	return err
}

// DiagStats sees EVERY stream, including unknown-service/unknown-method and
// malformed names that never reach an interceptor or handler.
type DiagStats struct{}

type diagMethodKey struct{}

func (d *DiagStats) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	return context.WithValue(ctx, diagMethodKey{}, info.FullMethodName)
}
func (d *DiagStats) HandleRPC(ctx context.Context, s stats.RPCStats) {
	method, _ := ctx.Value(diagMethodKey{}).(string)
	switch v := s.(type) {
	case *stats.InHeader:
		log.Printf("[DIAG-STATS] BEGIN peer=%s method=%s", v.RemoteAddr, v.FullMethod)
	case *stats.End:
		st, _ := status.FromError(v.Error)
		log.Printf("[DIAG-STATS] END method=%s code=%s dur=%s err=%v",
			method, st.Code(), v.EndTime.Sub(v.BeginTime).Round(time.Millisecond), v.Error)
	}
}
func (d *DiagStats) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context { return ctx }
func (d *DiagStats) HandleConn(context.Context, stats.ConnStats)                       {}
