package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/pairproxy/pairproxy/internal/db"
)

const (
	defaultReportInterval  = 30 * time.Second
	defaultRegisterPath    = "/api/internal/register"
	defaultUsageReportPath = "/api/internal/usage"
)

// Reporter 运行在 sp-2 上，周期性地：
//  1. 向 sp-1 发送心跳注册（POST /api/internal/register）
//  2. 批量上报本地采集的 usage 记录（POST /api/internal/usage）
type Reporter struct {
	logger         *zap.Logger
	sp1Addr        string // sp-1 地址，如 "http://sp-1:9000"
	selfID         string // 本节点 ID，如 "sp-2"
	selfAddr       string // 本节点对外地址，如 "http://sp-2:9000"
	selfWeight     int
	interval       time.Duration
	client         *http.Client
	usageRepo      *db.UsageRepo // 从本地 DB 读取待上报记录（可为 nil，则不上报用量）
	sharedSecret   string        // 用于对 sp-1 内部 API 鉴权
}

// ReporterConfig 配置 Reporter。
type ReporterConfig struct {
	SP1Addr      string
	SelfID       string
	SelfAddr     string
	SelfWeight   int
	Interval     time.Duration
	SharedSecret string // 内部 API 共享密钥（Bearer token）
}

// NewReporter 创建 Reporter。
func NewReporter(logger *zap.Logger, cfg ReporterConfig, usageRepo *db.UsageRepo) *Reporter {
	interval := cfg.Interval
	if interval <= 0 {
		interval = defaultReportInterval
	}
	weight := cfg.SelfWeight
	if weight <= 0 {
		weight = 1
	}
	return &Reporter{
		logger:       logger.Named("reporter"),
		sp1Addr:      cfg.SP1Addr,
		selfID:       cfg.SelfID,
		selfAddr:     cfg.SelfAddr,
		selfWeight:   weight,
		interval:     interval,
		client:       &http.Client{Timeout: 10 * time.Second},
		usageRepo:    usageRepo,
		sharedSecret: cfg.SharedSecret,
	}
}

// Start 启动后台上报 goroutine。
func (r *Reporter) Start(ctx context.Context) {
	go r.loop(ctx)
}

func (r *Reporter) loop(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	// 启动时立即注册一次
	r.sendHeartbeat()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.sendHeartbeat()
		}
	}
}

// RegisterPayload 心跳注册请求体。
type RegisterPayload struct {
	ID         string `json:"id"`
	Addr       string `json:"addr"`
	Weight     int    `json:"weight"`
	SourceNode string `json:"source_node"`
}

func (r *Reporter) sendHeartbeat() {
	payload := RegisterPayload{
		ID:         r.selfID,
		Addr:       r.selfAddr,
		Weight:     r.selfWeight,
		SourceNode: r.selfID,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		r.logger.Error("failed to marshal register payload", zap.Error(err))
		return
	}

	url := r.sp1Addr + defaultRegisterPath
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		r.logger.Error("failed to create register request", zap.Error(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if r.sharedSecret != "" {
		req.Header.Set("Authorization", "Bearer "+r.sharedSecret)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		r.logger.Warn("heartbeat failed", zap.String("sp1", r.sp1Addr), zap.Error(err))
		return
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		r.logger.Warn("heartbeat non-200",
			zap.String("sp1", r.sp1Addr),
			zap.Int("status", resp.StatusCode),
		)
		return
	}

	r.logger.Debug("heartbeat sent", zap.String("sp1", r.sp1Addr))
}

// UsageReportPayload 用量批量上报请求体。
type UsageReportPayload struct {
	SourceNode string           `json:"source_node"`
	Records    []db.UsageRecord `json:"records"`
}

// ReportUsage 立即上报一批 usage 记录（供调用方手动调用或测试）。
func (r *Reporter) ReportUsage(records []db.UsageRecord) error {
	if len(records) == 0 {
		return nil
	}

	payload := UsageReportPayload{
		SourceNode: r.selfID,
		Records:    records,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal usage payload: %w", err)
	}

	url := r.sp1Addr + defaultUsageReportPath
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create usage report request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if r.sharedSecret != "" {
		req.Header.Set("Authorization", "Bearer "+r.sharedSecret)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("usage report request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("usage report: unexpected status %d", resp.StatusCode)
	}

	r.logger.Debug("usage records reported",
		zap.String("sp1", r.sp1Addr),
		zap.Int("count", len(records)),
	)
	return nil
}
