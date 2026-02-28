package otel

import (
	"context"
	"testing"

	gotel "go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/zap/zaptest"
)

// ---------------------------------------------------------------------------
// TestOTelSetup_Disabled — disabled 时注册 noop provider
// ---------------------------------------------------------------------------

func TestOTelSetup_Disabled(t *testing.T) {
	logger := zaptest.NewLogger(t)

	shutdown, err := Setup(context.Background(), TelemetryConfig{Enabled: false}, logger)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer shutdown(context.Background())

	// noop provider 产生的 span 不可录制
	_, span := gotel.Tracer("test").Start(context.Background(), "test-span")
	defer span.End()

	if span.IsRecording() {
		t.Error("expected noop span (not recording) when disabled")
	}
}

// ---------------------------------------------------------------------------
// TestOTelSetup_Stdout — stdout exporter 能正常初始化
// ---------------------------------------------------------------------------

func TestOTelSetup_Stdout(t *testing.T) {
	logger := zaptest.NewLogger(t)

	shutdown, err := Setup(context.Background(), TelemetryConfig{
		Enabled:      true,
		OTLPProtocol: "stdout",
		ServiceName:  "test-service",
		SamplingRate: 1.0,
	}, logger)
	if err != nil {
		t.Fatalf("Setup stdout: %v", err)
	}
	defer shutdown(context.Background())

	// stdout exporter 的 span 应为可录制
	_, span := gotel.Tracer("test").Start(context.Background(), "test-span")
	defer span.End()

	if !span.IsRecording() {
		t.Error("expected recording span for stdout exporter")
	}
}

// ---------------------------------------------------------------------------
// TestOTelSetup_InvalidProtocol — 未知协议返回错误
// ---------------------------------------------------------------------------

func TestOTelSetup_InvalidProtocol(t *testing.T) {
	logger := zaptest.NewLogger(t)

	_, err := Setup(context.Background(), TelemetryConfig{
		Enabled:      true,
		OTLPProtocol: "websocket", // 未知协议
	}, logger)
	if err == nil {
		t.Error("expected error for unknown protocol, got nil")
	}
}

// ---------------------------------------------------------------------------
// TestOTelSetup_InMemory — InMemoryExporter 验证 span attributes
// ---------------------------------------------------------------------------

// customExporter 是一个简单的测试用 span exporter，不依赖 tracetest 包。
type customExporter struct {
	spans []sdktrace.ReadOnlySpan
}

func (e *customExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	e.spans = append(e.spans, spans...)
	return nil
}
func (e *customExporter) Shutdown(_ context.Context) error { return nil }

func TestOTelSetup_InMemory(t *testing.T) {
	exp := &customExporter{}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exp)),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	tracer := tp.Tracer("test")
	_, span := tracer.Start(context.Background(), "my-operation")
	span.End()

	if len(exp.spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(exp.spans))
	}
	if exp.spans[0].Name() != "my-operation" {
		t.Errorf("span name = %q, want my-operation", exp.spans[0].Name())
	}
}

// ---------------------------------------------------------------------------
// TestTracer — Tracer() 返回可用 tracer
// ---------------------------------------------------------------------------

func TestTracer(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// 使用 disabled 模式（noop）
	shutdown, _ := Setup(context.Background(), TelemetryConfig{Enabled: false}, logger)
	defer shutdown(context.Background())

	tr := Tracer("test-tracer")
	if tr == nil {
		t.Error("Tracer() should never return nil")
	}
}
