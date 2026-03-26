// Package api HTTP API 和 WebSocket 实时报告
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/l17728/pairproxy/tools/loadtest/internal/controller"
	"github.com/l17728/pairproxy/tools/loadtest/internal/metrics"
)

// Server HTTP API 服务器
type Server struct {
	controller *controller.Controller
	metrics    *metrics.Collector
	logger     *zap.Logger
	upgrader   websocket.Upgrader

	// WebSocket 客户端
	wsClients map[*websocket.Conn]bool
	wsMu      sync.RWMutex
}

// NewServer 创建 API 服务器
func NewServer(ctrl *controller.Controller, metricsCollector *metrics.Collector, logger *zap.Logger) *Server {
	return &Server{
		controller: ctrl,
		metrics:    metricsCollector,
		logger:     logger,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true // 允许所有来源
			},
		},
		wsClients: make(map[*websocket.Conn]bool),
	}
}

// Start 启动 HTTP 服务器
func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()

	// REST API
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/metrics", s.handleMetrics)
	mux.HandleFunc("/api/start", s.handleStart)
	mux.HandleFunc("/api/stop", s.handleStop)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/report", s.handleReport)

	// WebSocket
	mux.HandleFunc("/ws", s.handleWebSocket)

	// 静态文件（Dashboard）
	mux.Handle("/", http.FileServer(http.Dir("./web")))

	s.logger.Info("API server starting", zap.String("addr", addr))
	return http.ListenAndServe(addr, mux)
}

// handleStatus 返回当前测试状态
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status := struct {
		Running        bool      `json:"running"`
		CurrentWorkers int       `json:"current_workers"`
		StartTime      time.Time `json:"start_time,omitempty"`
		Duration       string    `json:"duration"`
	}{
		Running:  s.controller.IsRunning(),
		Duration: time.Since(s.controller.GetStartTime()).String(),
	}

	if workers, _, _, _ := s.controller.GetWorkerStats(); workers > 0 {
		status.CurrentWorkers = workers
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// handleMetrics 返回 Prometheus 格式的指标
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 生成 Prometheus 格式指标
	report := s.metrics.GetSnapshot(s.controller.GetCurrentWorkers())

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")

	fmt.Fprintf(w, "# HELP loadtest_requests_total Total requests\n")
	fmt.Fprintf(w, "# TYPE loadtest_requests_total counter\n")
	fmt.Fprintf(w, "loadtest_requests_total %d\n\n", report.TotalRequests)

	fmt.Fprintf(w, "# HELP loadtest_requests_success Successful requests\n")
	fmt.Fprintf(w, "# TYPE loadtest_requests_success counter\n")
	fmt.Fprintf(w, "loadtest_requests_success %d\n\n", report.SuccessCount)

	fmt.Fprintf(w, "# HELP loadtest_requests_failed Failed requests\n")
	fmt.Fprintf(w, "# TYPE loadtest_requests_failed counter\n")
	fmt.Fprintf(w, "loadtest_requests_failed %d\n\n", report.FailureCount)

	fmt.Fprintf(w, "# HELP loadtest_success_rate Success rate\n")
	fmt.Fprintf(w, "# TYPE loadtest_success_rate gauge\n")
	fmt.Fprintf(w, "loadtest_success_rate %.4f\n\n", report.SuccessRate)

	fmt.Fprintf(w, "# HELP loadtest_rps Requests per second\n")
	fmt.Fprintf(w, "# TYPE loadtest_rps gauge\n")
	fmt.Fprintf(w, "loadtest_rps %.4f\n\n", report.ThroughputRPS)

	fmt.Fprintf(w, "# HELP loadtest_latency_ms Request latency in milliseconds\n")
	fmt.Fprintf(w, "# TYPE loadtest_latency_ms summary\n")
	fmt.Fprintf(w, "loadtest_latency_ms{quantile=\"0.5\"} %.4f\n", report.LatencyStats.P50)
	fmt.Fprintf(w, "loadtest_latency_ms{quantile=\"0.9\"} %.4f\n", report.LatencyStats.P90)
	fmt.Fprintf(w, "loadtest_latency_ms{quantile=\"0.95\"} %.4f\n", report.LatencyStats.P95)
	fmt.Fprintf(w, "loadtest_latency_ms{quantile=\"0.99\"} %.4f\n\n", report.LatencyStats.P99)

	fmt.Fprintf(w, "# HELP loadtest_workers_active Active workers\n")
	fmt.Fprintf(w, "# TYPE loadtest_workers_active gauge\n")
	fmt.Fprintf(w, "loadtest_workers_active %d\n\n", report.TotalWorkers)
}

// handleWebSocket WebSocket 实时报告
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("WebSocket upgrade failed", zap.Error(err))
		return
	}
	defer conn.Close()

	// 注册客户端
	s.wsMu.Lock()
	s.wsClients[conn] = true
	s.wsMu.Unlock()

	defer func() {
		s.wsMu.Lock()
		delete(s.wsClients, conn)
		s.wsMu.Unlock()
	}()

	s.logger.Info("WebSocket client connected", zap.RemoteAddr(conn.RemoteAddr()))

	// 发送初始数据
	s.sendRealtimeMetrics(conn)

	// 持续推送
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := s.sendRealtimeMetrics(conn); err != nil {
				s.logger.Debug("WebSocket send failed", zap.Error(err))
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

// sendRealtimeMetrics 发送实时指标
func (s *Server) sendRealtimeMetrics(conn *websocket.Conn) error {
	report := s.metrics.GetSnapshot(s.controller.GetCurrentWorkers())

	data := struct {
		Timestamp     time.Time `json:"timestamp"`
		ActiveWorkers int       `json:"active_workers"`
		TotalRequests int64     `json:"total_requests"`
		SuccessRate   float64   `json:"success_rate"`
		RPS           float64   `json:"rps"`
		P50Latency    float64   `json:"p50_latency_ms"`
		P95Latency    float64   `json:"p95_latency_ms"`
		P99Latency    float64   `json:"p99_latency_ms"`
	}{
		Timestamp:     time.Now(),
		ActiveWorkers: report.TotalWorkers,
		TotalRequests: report.TotalRequests,
		SuccessRate:   report.SuccessRate,
		RPS:           report.ThroughputRPS,
		P50Latency:    report.LatencyStats.P50,
		P95Latency:    report.LatencyStats.P95,
		P99Latency:    report.LatencyStats.P99,
	}

	return conn.WriteJSON(data)
}

// BroadcastMetrics 广播指标到所有 WebSocket 客户端
func (s *Server) BroadcastMetrics(report *metrics.Report) {
	s.wsMu.RLock()
	clients := make([]*websocket.Conn, 0, len(s.wsClients))
	for conn := range s.wsClients {
		clients = append(clients, conn)
	}
	s.wsMu.RUnlock()

	data := struct {
		Type          string    `json:"type"`
		Timestamp     time.Time `json:"timestamp"`
		ActiveWorkers int       `json:"active_workers"`
		RPS           float64   `json:"rps"`
		SuccessRate   float64   `json:"success_rate"`
		AvgLatency    float64   `json:"avg_latency_ms"`
	}{
		Type:          "realtime",
		Timestamp:     time.Now(),
		ActiveWorkers: report.TotalWorkers,
		RPS:           report.ThroughputRPS,
		SuccessRate:   report.SuccessRate,
		AvgLatency:    report.LatencyStats.Mean,
	}

	for _, conn := range clients {
		if err := conn.WriteJSON(data); err != nil {
			// 客户端断开，清理
			s.wsMu.Lock()
			delete(s.wsClients, conn)
			s.wsMu.Unlock()
			conn.Close()
		}
	}
}

// handleStart 远程启动测试
func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.controller.IsRunning() {
		http.Error(w, "Test already running", http.StatusConflict)
		return
	}

	// 解析配置
	var req struct {
		Mode     string `json:"mode"`
		Workers  int    `json:"workers"`
		Duration string `json:"duration"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	// 应用配置
	if req.Mode != "" {
		s.controller.SetMode(req.Mode)
	}
	if req.Workers > 0 {
		s.controller.SetFixedWorkers(req.Workers)
	}
	if req.Duration != "" {
		duration, err := time.ParseDuration(req.Duration)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid duration: %v", err), http.StatusBadRequest)
			return
		}
		s.controller.SetDuration(duration)
	}

	// 启动测试
	go func() {
		if err := s.controller.Run(context.Background()); err != nil {
			s.logger.Error("Test failed", zap.Error(err))
		}
	}()

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "started",
	})
}

// handleStop 远程停止测试
func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.controller.Stop()

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "stopped",
	})
}

// handleConfig 获取/更新配置
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// 返回当前配置
		config := s.controller.GetConfig()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(config)

	case http.MethodPut:
		// 更新配置
		var newConfig controller.Config
		if err := json.NewDecoder(r.Body).Decode(&newConfig); err != nil {
			http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
			return
		}
		s.controller.UpdateConfig(&newConfig)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "updated",
		})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleReport 获取测试报告
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	report, err := s.controller.GenerateReport()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to generate report: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}
