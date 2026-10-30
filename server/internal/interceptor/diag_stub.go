//go:build !diag

// Package interceptor — production stub for the diagnostic hooks.
//
// TODO(diag): rollback = delete diag.go + diag_stub.go and remove the
// interceptor.DiagServerOptions() call in cmd/lunar-tear/grpc.go.
package interceptor

import "google.golang.org/grpc"

// DiagServerOptions is a no-op outside -tags diag builds.
func DiagServerOptions() []grpc.ServerOption { return nil }
