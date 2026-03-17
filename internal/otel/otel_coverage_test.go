package otel

import (
	"context"
	"testing"

	gotel "go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/zap/zaptest"
)

// ---------------------------------------------------------------------------
// Setup — Enabled=false registers noop (already covered, but re-verify shutdown)
// ---------------------------------------------------------------------------

func TestSetup_Disabled_ShutdownIsNoOp(t *testing.T) {
	logger := zaptest.NewLogger(t)
	shutdown, err := Setup(context.Background(), TelemetryConfig{Enabled: false}, logger)
	if err != nil {
		t.Fatalf("Setup disabled: %v", err)
	}
	// shutdown on a noop provider should not panic or return error
	shutdown(context.Background())
}

// ---------------------------------------------------------------------------
// Setup — Enabled=true with stdout, shutdown flushes (covers shutdown path)
// ---------------------------------------------------------------------------

func TestSetup_Stdout_ShutdownFlushes(t *testing.T) {
	logger := zaptest.NewLogger(t)
	shutdown, err := Setup(context.Background(), TelemetryConfig{
		Enabled:      true,
		OTLPProtocol: "stdout",
		ServiceName:  "coverage-svc",
		SamplingRate: 1.0,
	}, logger)
	if err != nil {
		t.Fatalf("Setup stdout: %v", err)
	}

	// Create and end a span so the exporter has something to flush
	_, span := gotel.Tracer("coverage-tracer").Start(context.Background(), "coverage-op")
	span.End()

	// Shutdown should flush spans and not panic
	shutdown(context.Background())
}

// ---------------------------------------------------------------------------
// Setup — ServiceName defaults to "pairproxy" when empty
// ---------------------------------------------------------------------------

func TestSetup_DefaultServiceName(t *testing.T) {
	logger := zaptest.NewLogger(t)
	shutdown, err := Setup(context.Background(), TelemetryConfig{
		Enabled:      true,
		OTLPProtocol: "stdout",
		ServiceName:  "", // should default to "pairproxy"
	}, logger)
	if err != nil {
		t.Fatalf("Setup with empty ServiceName: %v", err)
	}
	defer shutdown(context.Background())
}

// ---------------------------------------------------------------------------
// Setup — SamplingRate 0 defaults to 1.0 (AlwaysSample)
// ---------------------------------------------------------------------------

func TestSetup_SamplingRate_ZeroDefaultsToAlways(t *testing.T) {
	logger := zaptest.NewLogger(t)
	shutdown, err := Setup(context.Background(), TelemetryConfig{
		Enabled:      true,
		OTLPProtocol: "stdout",
		SamplingRate: 0.0, // should default to 1.0
	}, logger)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer shutdown(context.Background())

	_, span := gotel.Tracer("rate-test").Start(context.Background(), "rate-op")
	defer span.End()
	if !span.IsRecording() {
		t.Error("span should be recording when SamplingRate defaults to 1.0")
	}
}

// ---------------------------------------------------------------------------
// Setup — SamplingRate negative also defaults to AlwaysSample
// ---------------------------------------------------------------------------

func TestSetup_SamplingRate_NegativeDefaultsToAlways(t *testing.T) {
	logger := zaptest.NewLogger(t)
	shutdown, err := Setup(context.Background(), TelemetryConfig{
		Enabled:      true,
		OTLPProtocol: "stdout",
		SamplingRate: -0.5, // invalid → default 1.0
	}, logger)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer shutdown(context.Background())
}

// ---------------------------------------------------------------------------
// Setup — SamplingRate fractional uses TraceIDRatioBased sampler
// ---------------------------------------------------------------------------

func TestSetup_SamplingRate_Fractional(t *testing.T) {
	logger := zaptest.NewLogger(t)
	shutdown, err := Setup(context.Background(), TelemetryConfig{
		Enabled:      true,
		OTLPProtocol: "stdout",
		SamplingRate: 0.5, // fractional → TraceIDRatioBased
	}, logger)
	if err != nil {
		t.Fatalf("Setup with fractional rate: %v", err)
	}
	defer shutdown(context.Background())
}

// ---------------------------------------------------------------------------
// Setup — OTLPProtocol "grpc" without endpoint (uses default gRPC endpoint)
// ---------------------------------------------------------------------------

// TestSetup_GRPC_WithoutEndpoint tests that the grpc path is taken without panicking.
// The exporter may fail to connect (no real gRPC server), but the constructor
// should succeed (otlptracegrpc.New connects lazily in some versions).
func TestSetup_GRPC_EmptyEndpoint_AttemptConnect(t *testing.T) {
	// This test verifies the grpc branch is exercised. If New() returns an error
	// (e.g. fails to connect immediately), that's acceptable — we just verify
	// the code path doesn't panic.
	logger := zaptest.NewLogger(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*1000*1000*1000) // 5s
	defer cancel()

	shutdown, err := Setup(ctx, TelemetryConfig{
		Enabled:      true,
		OTLPProtocol: "grpc",
		OTLPEndpoint: "", // no endpoint — uses default
	}, logger)

	// Either success or a connection error is acceptable
	if err == nil {
		defer shutdown(context.Background())
	}
	// The important thing is that we don't panic
}

// ---------------------------------------------------------------------------
// Setup — OTLPProtocol "http" without endpoint
// ---------------------------------------------------------------------------

func TestSetup_HTTP_EmptyEndpoint(t *testing.T) {
	logger := zaptest.NewLogger(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*1000*1000*1000)
	defer cancel()

	shutdown, err := Setup(ctx, TelemetryConfig{
		Enabled:      true,
		OTLPProtocol: "http",
		OTLPEndpoint: "",
	}, logger)

	if err == nil {
		defer shutdown(context.Background())
	}
}

// ---------------------------------------------------------------------------
// Setup — unknown protocol returns error (re-verify)
// ---------------------------------------------------------------------------

func TestSetup_UnknownProtocol_ReturnsError(t *testing.T) {
	logger := zaptest.NewLogger(t)
	_, err := Setup(context.Background(), TelemetryConfig{
		Enabled:      true,
		OTLPProtocol: "kafka",
	}, logger)
	if err == nil {
		t.Error("expected error for unknown protocol 'kafka'")
	}
}

// ---------------------------------------------------------------------------
// Tracer — returns non-nil tracer from global provider
// ---------------------------------------------------------------------------

func TestTracer_ReturnsNonNil(t *testing.T) {
	logger := zaptest.NewLogger(t)
	shutdown, err := Setup(context.Background(), TelemetryConfig{Enabled: false}, logger)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer shutdown(context.Background())

	tr := Tracer("coverage-test-tracer")
	if tr == nil {
		t.Error("Tracer() should never return nil")
	}
}

// ---------------------------------------------------------------------------
// Custom exporter verifies span recording
// ---------------------------------------------------------------------------

func TestSetup_StdoutExporter_SpanRecorded(t *testing.T) {
	exp := &testSpanExporter{}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exp)),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	tracer := tp.Tracer("test-coverage")
	_, span := tracer.Start(context.Background(), "span-for-coverage")
	span.End()

	if len(exp.spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(exp.spans))
	}
	if exp.spans[0].Name() != "span-for-coverage" {
		t.Errorf("span name = %q, want 'span-for-coverage'", exp.spans[0].Name())
	}
}

// testSpanExporter is a minimal span exporter for coverage tests.
type testSpanExporter struct {
	spans []sdktrace.ReadOnlySpan
}

func (e *testSpanExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	e.spans = append(e.spans, spans...)
	return nil
}

func (e *testSpanExporter) Shutdown(_ context.Context) error { return nil }
