// Package otel 封装 OpenTelemetry SDK 初始化逻辑。
// 调用 Setup 后，进程内所有通过 go.opentelemetry.io/otel 获取的 Tracer 都会使用
// 全局 TracerProvider，无需显式传递 TracerProvider 实例。
package otel

import (
	"context"
	"fmt"

	gotel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// TelemetryConfig OpenTelemetry 分布式追踪配置。
type TelemetryConfig struct {
	// Enabled 是否启用 OTel 追踪，默认 false（零开销）
	Enabled bool
	// OTLPEndpoint OTLP 接收端地址（如 "http://jaeger:4318"）
	OTLPEndpoint string
	// OTLPProtocol 传输协议："grpc"（默认）| "http" | "stdout"
	OTLPProtocol string
	// ServiceName 服务名称，显示在 Jaeger 等追踪后端
	ServiceName string
	// SamplingRate 采样率 0.0~1.0，默认 1.0（全量采样）
	SamplingRate float64
}

// Setup 初始化全局 TracerProvider。
//
//   - 若 cfg.Enabled=false，注册 noop TracerProvider（零开销），直接返回。
//   - 否则根据 cfg.OTLPProtocol 创建对应 exporter 并启动 TracerProvider。
//
// 返回的 shutdown 函数必须在进程退出前调用（通常在 defer 或信号处理中），以确保
// 内存中未上报的 span 被 flush 到后端。
func Setup(ctx context.Context, cfg TelemetryConfig, logger *zap.Logger) (shutdown func(context.Context), err error) {
	noop := func(_ context.Context) {}

	if !cfg.Enabled {
		logger.Info("otel: tracing disabled (noop provider)")
		gotel.SetTracerProvider(trace.NewNoopTracerProvider())
		return noop, nil
	}

	// 创建 resource（描述本服务）
	svcName := cfg.ServiceName
	if svcName == "" {
		svcName = "pairproxy"
	}
	res, err := resource.New(ctx,
		resource.WithSchemaURL(semconv.SchemaURL),
		resource.WithAttributes(semconv.ServiceName(svcName)),
	)
	if err != nil {
		return noop, fmt.Errorf("otel: create resource: %w", err)
	}

	// 创建 exporter
	var exp sdktrace.SpanExporter
	protocol := cfg.OTLPProtocol
	if protocol == "" {
		protocol = "grpc"
	}

	switch protocol {
	case "grpc":
		opts := []otlptracegrpc.Option{}
		if cfg.OTLPEndpoint != "" {
			opts = append(opts, otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint), otlptracegrpc.WithInsecure())
		}
		exp, err = otlptracegrpc.New(ctx, opts...)
	case "http":
		opts := []otlptracehttp.Option{}
		if cfg.OTLPEndpoint != "" {
			opts = append(opts, otlptracehttp.WithEndpoint(cfg.OTLPEndpoint), otlptracehttp.WithInsecure())
		}
		exp, err = otlptracehttp.New(ctx, opts...)
	case "stdout":
		exp, err = stdouttrace.New(stdouttrace.WithPrettyPrint())
	default:
		return noop, fmt.Errorf("otel: unknown protocol %q (valid: grpc, http, stdout)", protocol)
	}
	if err != nil {
		return noop, fmt.Errorf("otel: create %s exporter: %w", protocol, err)
	}

	// 采样率
	rate := cfg.SamplingRate
	if rate <= 0 || rate > 1.0 {
		rate = 1.0
	}
	var sampler sdktrace.Sampler
	if rate >= 1.0 {
		sampler = sdktrace.AlwaysSample()
	} else {
		sampler = sdktrace.TraceIDRatioBased(rate)
	}

	// 创建 TracerProvider
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithSampler(sampler),
		sdktrace.WithResource(res),
	)

	// 注册为全局 provider
	gotel.SetTracerProvider(tp)

	logger.Info("otel: tracing initialized",
		zap.String("protocol", protocol),
		zap.String("endpoint", cfg.OTLPEndpoint),
		zap.String("service_name", svcName),
		zap.Float64("sampling_rate", rate),
	)

	return func(ctx context.Context) {
		if err := tp.Shutdown(ctx); err != nil {
			logger.Error("otel: shutdown failed", zap.Error(err))
		}
	}, nil
}

// Tracer 返回指定名称的全局 Tracer。
// 是 go.opentelemetry.io/otel.Tracer 的便捷封装。
func Tracer(name string) trace.Tracer {
	return gotel.Tracer(name)
}
