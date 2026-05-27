package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/db"
)

const (
	defaultReportInterval  = 30 * time.Second
	defaultRegisterPath    = "/api/internal/register"
	defaultUsageReportPath = "/api/internal/usage"
	defaultMaxBatchSize    = 1000
)

// Reporter 运行在 sp-2 上，周期性地：
//  1. 向 sp-1 发送心跳注册（POST /api/internal/register）
//  2. 批量上报本地采集的 usage 记录（POST /api/internal/usage）
type Reporter struct {
	logger       *zap.Logger
	sp1Addr      string // sp-1 地址，如 "http://sp-1:9000"
	selfID       string // 本节点 ID，如 "sp-2"
	selfAddr     string // 本节点对外地址，如 "http://sp-2:9000"
	selfWeight   int
	interval     time.Duration
	client       *http.Client
	usageRepo    *db.UsageRepo // 从本地 DB 读取待上报记录（可为 nil，则不上报用量）
	sharedSecret string        // 用于对 sp-1 内部 API 鉴权
	maxBatch     int           // 每批最多上报条数

	// 可观测性指标（原子操作，供 /metrics 端点读取）
	heartbeatFailures atomic.Int64 // 心跳失败累计次数
	lastLatencyMs     atomic.Int64 // 最近一次心跳的延迟（毫秒），-1 表示从未成功
	usageReportFails  atomic.Int64 // 用量上报失败累计次数（改进项2）
	pendingRecords    atomic.Int64 // 当前待上报记录数（改进项2）

	done chan struct{} // closed when loop exits
}

// HeartbeatFailures 返回累计心跳失败次数。
func (r *Reporter) HeartbeatFailures() int64 { return r.heartbeatFailures.Load() }

// LastLatencyMs 返回最近一次成功心跳的延迟（毫秒），-1 表示从未成功。
func (r *Reporter) LastLatencyMs() int64 { return r.lastLatencyMs.Load() }

// UsageReportFails 返回累计用量上报失败次数。
func (r *Reporter) UsageReportFails() int64 { return r.usageReportFails.Load() }

// PendingRecords 返回当前待上报记录数（上次查询时的快照）。
func (r *Reporter) PendingRecords() int64 { return r.pendingRecords.Load() }

// ReporterConfig 配置 Reporter。
type ReporterConfig struct {
	SP1Addr      string
	SelfID       string
	SelfAddr     string
	SelfWeight   int
	Interval     time.Duration
	SharedSecret string // 内部 API 共享密钥（Bearer token）
	MaxBatch     int    // 每批最多上报条数（改进项2），0=使用默认值 1000
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
	maxBatch := cfg.MaxBatch
	if maxBatch <= 0 {
		maxBatch = defaultMaxBatchSize
	}
	r := &Reporter{
		logger:       logger.Named("reporter"),
		sp1Addr:      cfg.SP1Addr,
		selfID:       cfg.SelfID,
		selfAddr:     cfg.SelfAddr,
		selfWeight:   weight,
		interval:     interval,
		client:       &http.Client{Timeout: 10 * time.Second},
		usageRepo:    usageRepo,
		sharedSecret: cfg.SharedSecret,
		maxBatch:     maxBatch,
	}
	r.lastLatencyMs.Store(-1) // -1 表示从未成功
	r.done = make(chan struct{})
	return r
}

// Start 启动后台上报 goroutine。
func (r *Reporter) Start(ctx context.Context) {
	go func() {
		r.loop(ctx)
		close(r.done)
	}()
}

// Wait 阻塞直到后台 goroutine 退出（用于测试和 graceful shutdown）。
func (r *Reporter) Wait() {
	<-r.done
}

func (r *Reporter) loop(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	// 启动时立即注册一次
	r.sendHeartbeat(ctx)
	// 启动时立即尝试上报一次（改进项2）
	if r.usageRepo != nil {
		r.flushUsage(ctx)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.sendHeartbeat(ctx)
			// 每次心跳后也尝试上报用量（改进项2）
			if r.usageRepo != nil {
				r.flushUsage(ctx)
			}
		}
	}
}

// flushUsage 查询本地未上报记录并批量发送给 sp-1。
// 使用 ListUnsynced + MarkSynced 实现水印追踪，保证幂等性（RequestID 去重）。
func (r *Reporter) flushUsage(ctx context.Context) {
	logs, err := r.usageRepo.ListUnsynced(r.maxBatch)
	if err != nil {
		r.logger.Error("usage flush: failed to list unsynced records",
			zap.String("sp1", r.sp1Addr),
			zap.Error(err),
		)
		r.usageReportFails.Add(1)
		return
	}

	if len(logs) == 0 {
		r.pendingRecords.Store(0)
		r.logger.Debug("usage flush: no pending records")
		return
	}

	r.pendingRecords.Store(int64(len(logs)))
	r.logger.Info("usage flush: sending pending records",
		zap.String("sp1", r.sp1Addr),
		zap.Int("count", len(logs)),
	)

	// 将 UsageLog 转换为 UsageRecord（Reporter 使用 UsageRecord 格式上报）
	records := make([]db.UsageRecord, 0, len(logs))
	requestIDs := make([]string, 0, len(logs))
	for _, log := range logs {
		records = append(records, db.UsageRecord{
			RequestID:    log.RequestID,
			UserID:       log.UserID,
			Model:        log.Model,
			InputTokens:  log.InputTokens,
			OutputTokens: log.OutputTokens,
			IsStreaming:  log.IsStreaming,
			UpstreamURL:  log.UpstreamURL,
			StatusCode:   log.StatusCode,
			DurationMs:   log.DurationMs,
			TtftMs:       log.TtftMs,
			TpotMs:       log.TpotMs,
			SourceNode:   log.SourceNode,
			CreatedAt:    log.CreatedAt,
		})
		requestIDs = append(requestIDs, log.RequestID)
	}

	if err := r.ReportUsage(ctx, records); err != nil {
		r.logger.Warn("usage flush: failed to report to sp-1, will retry next cycle",
			zap.String("sp1", r.sp1Addr),
			zap.Int("count", len(records)),
			zap.Int64("total_fails", r.usageReportFails.Load()+1),
			zap.Error(err),
		)
		r.usageReportFails.Add(1)
		return
	}

	// 上报成功：标记为已同步
	if err := r.usageRepo.MarkSynced(requestIDs); err != nil {
		r.logger.Error("usage flush: failed to mark records as synced (will re-report next cycle)",
			zap.Int("count", len(requestIDs)),
			zap.Error(err),
		)
		// 不计入 usageReportFails，因为数据已成功发送到 sp-1
		return
	}

	r.pendingRecords.Store(0)
	r.logger.Info("usage flush: records synced successfully",
		zap.String("sp1", r.sp1Addr),
		zap.Int("count", len(records)),
	)
}

// RegisterPayload 心跳注册请求体。
type RegisterPayload struct {
	ID         string `json:"id"`
	Addr       string `json:"addr"`
	Weight     int    `json:"weight"`
	SourceNode string `json:"source_node"`
}

func (r *Reporter) sendHeartbeat(ctx context.Context) {
	payload := RegisterPayload{
		ID:         r.selfID,
		Addr:       r.selfAddr,
		Weight:     r.selfWeight,
		SourceNode: r.selfID,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		r.logger.Error("failed to marshal register payload", zap.Error(err))
		r.heartbeatFailures.Add(1)
		return
	}

	url := r.sp1Addr + defaultRegisterPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		r.logger.Error("failed to create register request", zap.Error(err))
		r.heartbeatFailures.Add(1)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if r.sharedSecret != "" {
		req.Header.Set("Authorization", "Bearer "+r.sharedSecret)
	}

	start := time.Now()
	resp, err := r.client.Do(req)
	if err != nil {
		r.logger.Warn("heartbeat failed", zap.String("sp1", r.sp1Addr), zap.Error(err))
		r.heartbeatFailures.Add(1)
		return
	}
	resp.Body.Close()
	latencyMs := time.Since(start).Milliseconds()

	if resp.StatusCode != http.StatusOK {
		r.logger.Warn("heartbeat non-200",
			zap.String("sp1", r.sp1Addr),
			zap.Int("status", resp.StatusCode),
		)
		r.heartbeatFailures.Add(1)
		return
	}

	r.lastLatencyMs.Store(latencyMs)
	r.logger.Debug("heartbeat sent",
		zap.String("sp1", r.sp1Addr),
		zap.Int64("latency_ms", latencyMs),
	)
}

// UsageReportPayload 用量批量上报请求体。
type UsageReportPayload struct {
	SourceNode string           `json:"source_node"`
	Records    []db.UsageRecord `json:"records"`
}

// ReportUsage 立即上报一批 usage 记录（供调用方手动调用或测试）。
func (r *Reporter) ReportUsage(ctx context.Context, records []db.UsageRecord) error {
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
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
