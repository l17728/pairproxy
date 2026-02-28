package db

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UsageFilter 用量查询条件
type UsageFilter struct {
	UserID  string
	GroupID string
	From    *time.Time
	To      *time.Time
	Model   string
	Limit   int
	Offset  int
}

// UsageRecord 用量写入数据（通过 channel 传递，不直接操作 DB）
type UsageRecord struct {
	RequestID    string
	UserID       string
	Model        string
	InputTokens  int
	OutputTokens int
	IsStreaming  bool
	UpstreamURL  string
	StatusCode   int
	DurationMs   int64
	SourceNode   string
	CreatedAt    time.Time
}

// CostFunc 费用计算函数类型（model, inputTokens, outputTokens → USD）
type CostFunc func(model string, inputTokens, outputTokens int) float64

// UsageWriter 异步批量写入用量日志
type UsageWriter struct {
	db         *gorm.DB
	logger     *zap.Logger
	ch         chan UsageRecord
	bufferSize int
	interval   time.Duration
	done       chan struct{} // closed when runLoop exits
	costFn     CostFunc     // 可选：用于计算 cost_usd（nil 则不计算）
}

// NewUsageWriter 创建 UsageWriter
// bufferSize: channel 容量，也是批量写入的最大条数
// interval: 强制 flush 间隔
func NewUsageWriter(db *gorm.DB, logger *zap.Logger, bufferSize int, interval time.Duration) *UsageWriter {
	if bufferSize <= 0 {
		bufferSize = 200
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	w := &UsageWriter{
		db:         db,
		logger:     logger.Named("usage_writer"),
		ch:         make(chan UsageRecord, bufferSize*2), // 双倍 buffer 避免频繁阻塞
		bufferSize: bufferSize,
		interval:   interval,
		done:       make(chan struct{}),
	}
	return w
}

// SetCostFunc 设置费用计算函数（可选；nil 时不计算费用）
func (w *UsageWriter) SetCostFunc(fn CostFunc) {
	w.costFn = fn
}

// Start 启动后台写入 goroutine（ctx 取消时停止）
func (w *UsageWriter) Start(ctx context.Context) {
	w.logger.Info("usage writer started",
		zap.Int("buffer_size", w.bufferSize),
		zap.Duration("flush_interval", w.interval),
	)
	go func() {
		w.runLoop(ctx)
		close(w.done)
	}()
}

// Wait 阻塞直到后台 goroutine 退出（用于测试和 graceful shutdown）
func (w *UsageWriter) Wait() {
	<-w.done
}

// QueueDepth 返回当前 channel 中待处理的用量记录数量。
// 此值反映背压（backpressure）：接近 channel 容量时应关注写入性能。
func (w *UsageWriter) QueueDepth() int {
	return len(w.ch)
}

// Record 非阻塞写入一条用量记录到 channel
// 如果 channel 已满，丢弃记录并记录警告（保证代理主路不阻塞）
func (w *UsageWriter) Record(r UsageRecord) {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	// 没有 token 数据的记录仍然写入（记录错误请求），无需特殊处理
	select {
	case w.ch <- r:
		w.logger.Debug("usage record queued",
			zap.String("request_id", r.RequestID),
			zap.String("user_id", r.UserID),
			zap.Int("input_tokens", r.InputTokens),
			zap.Int("output_tokens", r.OutputTokens),
		)
	default:
		w.logger.Warn("usage channel full, dropping record",
			zap.String("request_id", r.RequestID),
			zap.String("user_id", r.UserID),
		)
	}
}

// Flush 阻塞等待当前 channel 中所有记录写入完成（graceful shutdown 用）
func (w *UsageWriter) Flush() {
	w.logger.Info("flushing usage records", zap.Int("pending", len(w.ch)))
	// 排空 channel 并写入
	var batch []UsageRecord
	for {
		select {
		case r := <-w.ch:
			batch = append(batch, r)
		default:
			goto done
		}
	}
done:
	if len(batch) > 0 {
		w.writeBatch(batch)
	}
	w.logger.Info("usage flush completed", zap.Int("flushed", len(batch)))
}

// runLoop 后台写入主循环
func (w *UsageWriter) runLoop(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	var batch []UsageRecord

	flush := func() {
		if len(batch) == 0 {
			return
		}
		w.writeBatch(batch)
		batch = batch[:0] // 复用底层数组
	}

	for {
		select {
		case r := <-w.ch:
			batch = append(batch, r)
			// 达到批量大小立即写入
			if len(batch) >= w.bufferSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			// 停止时排空剩余记录
			w.logger.Info("usage writer stopping, draining channel")
		drain:
			for {
				select {
				case r := <-w.ch:
					batch = append(batch, r)
				default:
					break drain
				}
			}
			flush()
			w.logger.Info("usage writer stopped")
			return
		}
	}
}

// writeBatch 批量写入 DB（使用 INSERT OR IGNORE 保证幂等）
func (w *UsageWriter) writeBatch(batch []UsageRecord) {
	ctx := context.Background()
	_, span := otel.Tracer("pairproxy.db").Start(ctx, "pairproxy.db.write")
	span.SetAttributes(attribute.Int("batch_size", len(batch)))
	defer span.End()

	logs := make([]UsageLog, 0, len(batch))
	for _, r := range batch {
		total := r.InputTokens + r.OutputTokens
		var cost float64
		if w.costFn != nil {
			cost = w.costFn(r.Model, r.InputTokens, r.OutputTokens)
		}
		logs = append(logs, UsageLog{
			RequestID:    r.RequestID,
			UserID:       r.UserID,
			Model:        r.Model,
			InputTokens:  r.InputTokens,
			OutputTokens: r.OutputTokens,
			TotalTokens:  total,
			IsStreaming:  r.IsStreaming,
			UpstreamURL:  r.UpstreamURL,
			StatusCode:   r.StatusCode,
			DurationMs:   r.DurationMs,
			CostUSD:      cost,
			SourceNode:   r.SourceNode,
			CreatedAt:    r.CreatedAt,
		})
	}

	// OnConflict(DoNothing) 实现 INSERT OR IGNORE 语义
	result := w.db.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(logs, 100)
	if result.Error != nil {
		w.logger.Error("failed to write usage batch",
			zap.Int("count", len(logs)),
			zap.Error(result.Error),
		)
		return
	}
	w.logger.Debug("usage batch written",
		zap.Int("attempted", len(logs)),
		zap.Int64("inserted", result.RowsAffected),
	)
}

// TotalTokens 计算总 token 数（辅助方法，放在 UsageRecord 上）
func (r *UsageRecord) TotalTokens() int {
	return r.InputTokens + r.OutputTokens
}

// ---------------------------------------------------------------------------
// UsageRepo 用量查询仓库
// ---------------------------------------------------------------------------

// UsageRepo 提供用量日志的查询接口
type UsageRepo struct {
	db     *gorm.DB
	logger *zap.Logger
}

// NewUsageRepo 创建 UsageRepo
func NewUsageRepo(db *gorm.DB, logger *zap.Logger) *UsageRepo {
	return &UsageRepo{db: db, logger: logger.Named("usage_repo")}
}

// Query 查询用量日志（支持多条件过滤）
func (r *UsageRepo) Query(filter UsageFilter) ([]UsageLog, error) {
	query := r.db.Model(&UsageLog{})

	if filter.UserID != "" {
		query = query.Where("user_id = ?", filter.UserID)
	}
	if filter.Model != "" {
		query = query.Where("model = ?", filter.Model)
	}
	if filter.From != nil {
		query = query.Where("created_at >= ?", *filter.From)
	}
	if filter.To != nil {
		query = query.Where("created_at <= ?", *filter.To)
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	query = query.Order("created_at DESC").Limit(limit).Offset(filter.Offset)

	var logs []UsageLog
	if err := query.Find(&logs).Error; err != nil {
		r.logger.Error("failed to query usage logs", zap.Error(err))
		return nil, fmt.Errorf("query usage logs: %w", err)
	}

	r.logger.Debug("usage logs queried",
		zap.String("user_id", filter.UserID),
		zap.Int("count", len(logs)),
	)
	return logs, nil
}

// SumTokens 聚合指定用户在时间范围内的 token 总量
func (r *UsageRepo) SumTokens(userID string, from, to time.Time) (inputSum, outputSum int64, err error) {
	type result struct {
		InputSum  int64
		OutputSum int64
	}
	var res result
	err = r.db.Model(&UsageLog{}).
		Select("COALESCE(SUM(input_tokens), 0) as input_sum, COALESCE(SUM(output_tokens), 0) as output_sum").
		Where("user_id = ? AND created_at >= ? AND created_at <= ?", userID, from, to).
		Scan(&res).Error
	if err != nil {
		r.logger.Error("failed to sum tokens",
			zap.String("user_id", userID),
			zap.Error(err),
		)
		return 0, 0, fmt.Errorf("sum tokens for user %q: %w", userID, err)
	}
	return res.InputSum, res.OutputSum, nil
}

// GlobalStats 全局用量统计
type GlobalStats struct {
	TotalInput   int64
	TotalOutput  int64
	TotalTokens  int64 // = TotalInput + TotalOutput
	RequestCount int64
	ErrorCount   int64
}

// GlobalSumTokens 计算时间段内所有用户的汇总统计
func (r *UsageRepo) GlobalSumTokens(from, to time.Time) (GlobalStats, error) {
	type rawResult struct {
		TotalInput   int64
		TotalOutput  int64
		RequestCount int64
		ErrorCount   int64
	}
	var res rawResult
	err := r.db.Model(&UsageLog{}).
		Select(`COALESCE(SUM(input_tokens),0) as total_input,
			COALESCE(SUM(output_tokens),0) as total_output,
			COUNT(*) as request_count,
			SUM(CASE WHEN status_code != 200 THEN 1 ELSE 0 END) as error_count`).
		Where("created_at >= ? AND created_at <= ?", from, to).
		Scan(&res).Error
	if err != nil {
		r.logger.Error("failed to get global stats", zap.Error(err))
		return GlobalStats{}, fmt.Errorf("global sum tokens: %w", err)
	}
	return GlobalStats{
		TotalInput:   res.TotalInput,
		TotalOutput:  res.TotalOutput,
		TotalTokens:  res.TotalInput + res.TotalOutput,
		RequestCount: res.RequestCount,
		ErrorCount:   res.ErrorCount,
	}, nil
}

// UserStatRow 用户统计汇总行
type UserStatRow struct {
	UserID       string
	TotalInput   int64
	TotalOutput  int64
	RequestCount int64
}

// UserStats 按用户聚合 token 用量，按用量降序，最多 limit 条
func (r *UsageRepo) UserStats(from, to time.Time, limit int) ([]UserStatRow, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows []UserStatRow
	err := r.db.Model(&UsageLog{}).
		Select(`user_id,
			COALESCE(SUM(input_tokens),0) as total_input,
			COALESCE(SUM(output_tokens),0) as total_output,
			COUNT(*) as request_count`).
		Where("created_at >= ? AND created_at <= ?", from, to).
		Group("user_id").
		Order("total_input + total_output DESC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		r.logger.Error("failed to get user stats", zap.Error(err))
		return nil, fmt.Errorf("user stats: %w", err)
	}
	return rows, nil
}

// ExportLogs 以流式方式导出时间段内的所有用量日志，每条记录调用一次 fn 回调。
// 使用分批查询（pageSize 条/批）避免一次性加载全部数据占用大量内存。
// fn 返回非 nil error 时立即停止遍历并返回该 error（可用于提前中断）。
//
// 参数 pageSize 为 0 时使用默认值 500。
func (r *UsageRepo) ExportLogs(from, to time.Time, fn func(UsageLog) error) error {
	const defaultPageSize = 500
	pageSize := defaultPageSize
	offset := 0

	r.logger.Info("export logs started",
		zap.Time("from", from),
		zap.Time("to", to),
	)

	total := 0
	for {
		var batch []UsageLog
		err := r.db.Model(&UsageLog{}).
			Where("created_at >= ? AND created_at <= ?", from, to).
			Order("created_at ASC, id ASC").
			Limit(pageSize).
			Offset(offset).
			Find(&batch).Error
		if err != nil {
			r.logger.Error("export logs: query failed",
				zap.Int("offset", offset),
				zap.Error(err),
			)
			return fmt.Errorf("export logs query at offset %d: %w", offset, err)
		}
		if len(batch) == 0 {
			break
		}
		for _, log := range batch {
			if err := fn(log); err != nil {
				r.logger.Debug("export logs: callback interrupted",
					zap.Int("exported_so_far", total),
					zap.Error(err),
				)
				return err
			}
			total++
		}
		r.logger.Debug("export logs: batch done",
			zap.Int("batch_size", len(batch)),
			zap.Int("total_so_far", total),
			zap.Int("offset", offset),
		)
		if len(batch) < pageSize {
			break // 最后一批，不足 pageSize 条
		}
		offset += len(batch)
	}

	r.logger.Info("export logs completed", zap.Int("total", total))
	return nil
}

// SumCostUSD 计算时间段内的总估算费用（USD）
func (r *UsageRepo) SumCostUSD(from, to time.Time) (float64, error) {	var result struct{ Total float64 }
	err := r.db.Model(&UsageLog{}).
		Select("COALESCE(SUM(cost_usd), 0) as total").
		Where("created_at >= ? AND created_at <= ?", from, to).
		Scan(&result).Error
	if err != nil {
		r.logger.Error("failed to sum cost_usd", zap.Error(err))
		return 0, fmt.Errorf("sum cost_usd: %w", err)
	}
	return result.Total, nil
}

// ListUnsynced 查询未上报给 sp-1 的记录（sp-2+ 使用）
func (r *UsageRepo) ListUnsynced(limit int) ([]UsageLog, error) {
	if limit <= 0 {
		limit = 200
	}
	var logs []UsageLog
	if err := r.db.Where("synced = ?", false).Order("created_at ASC").Limit(limit).Find(&logs).Error; err != nil {
		r.logger.Error("failed to list unsynced usage logs", zap.Error(err))
		return nil, fmt.Errorf("list unsynced: %w", err)
	}
	r.logger.Debug("unsynced usage logs fetched", zap.Int("count", len(logs)))
	return logs, nil
}

// MarkSynced 将指定 request_id 列表标记为已上报
func (r *UsageRepo) MarkSynced(requestIDs []string) error {
	if len(requestIDs) == 0 {
		return nil
	}
	result := r.db.Model(&UsageLog{}).
		Where("request_id IN ?", requestIDs).
		Update("synced", true)
	if result.Error != nil {
		r.logger.Error("failed to mark usage as synced",
			zap.Int("count", len(requestIDs)),
			zap.Error(result.Error),
		)
		return fmt.Errorf("mark synced: %w", result.Error)
	}
	r.logger.Debug("usage records marked synced",
		zap.Int("requested", len(requestIDs)),
		zap.Int64("updated", result.RowsAffected),
	)
	return nil
}
