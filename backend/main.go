package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const Version = "v1.1.0"

type Event struct {
	ID        int             `json:"id"`
	EventType string          `json:"event_type"`
	Reason    string          `json:"reason"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type IngestRequest struct {
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
}

type SummaryResponse struct {
	TotalEvents    int `json:"total_events"`
	Denials        int `json:"denials"`
	StopRejections int `json:"stop_rejections"`
	Last24h        int `json:"last_24h"`
}

type TopTool struct {
	Tool  string `json:"tool"`
	Count int    `json:"count"`
}

type TimeseriesPoint struct {
	Timestamp      time.Time `json:"timestamp"`
	Total          int       `json:"total"`
	Denials        int       `json:"denials"`
	StopRejections int       `json:"stop_rejections"`
}

var (
	db *sql.DB

	attentionEventsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "attention_events_total",
			Help: "Total attention events recorded",
		},
		[]string{"event_type", "tool"},
	)

	attentionEventsLast24h = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "attention_events_last_24h",
			Help: "Number of attention events in the last 24 hours",
		},
	)

	httpRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total HTTP requests handled",
		},
		[]string{"method", "path", "status"},
	)

	httpRequestDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)
)

func init() {
	prometheus.MustRegister(attentionEventsTotal)
	prometheus.MustRegister(attentionEventsLast24h)
	prometheus.MustRegister(httpRequestsTotal)
	prometheus.MustRegister(httpRequestDurationSeconds)
}

type ipRateLimiter struct {
	sync.Mutex
	counts map[string]int
	resets map[string]time.Time
}

var limiter = &ipRateLimiter{
	counts: make(map[string]int),
	resets: make(map[string]time.Time),
}

func (l *ipRateLimiter) Allow(ip string) bool {
	l.Lock()
	defer l.Unlock()
	now := time.Now()
	reset, exists := l.resets[ip]
	if !exists || now.After(reset) {
		l.counts[ip] = 1
		l.resets[ip] = now.Add(time.Minute)
		return true
	}
	if l.counts[ip] >= 60 {
		return false
	}
	l.counts[ip]++
	return true
}

func getClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	if xrip := r.Header.Get("X-Real-IP"); xrip != "" {
		return strings.TrimSpace(xrip)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func checkAuth(r *http.Request) bool {
	apiKey := os.Getenv("METRICS_API_KEY")
	if apiKey == "" {
		return true
	}
	if r.Header.Get("X-API-Key") == apiKey {
		return true
	}
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") && strings.TrimPrefix(auth, "Bearer ") == apiKey {
		return true
	}
	return false
}

func extractToolName(payloadObj map[string]interface{}) string {
	if payloadObj == nil {
		return "none"
	}
	if tc, ok := payloadObj["toolCall"].(map[string]interface{}); ok {
		if name, ok := tc["name"].(string); ok && name != "" {
			return name
		}
	}
	if name, ok := payloadObj["tool_name"].(string); ok && name != "" {
		return name
	}
	if name, ok := payloadObj["tool"].(string); ok && name != "" {
		return name
	}
	return "none"
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func metricsMiddleware(path string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next(sw, r)
		duration := time.Since(start)
		statusStr := strconv.Itoa(sw.status)
		httpRequestsTotal.WithLabelValues(r.Method, path, statusStr).Inc()
		httpRequestDurationSeconds.WithLabelValues(r.Method, path).Observe(duration.Seconds())
	}
}

func startRetentionWorker(ctx context.Context, retentionDays int) {
	if retentionDays <= 0 {
		return
	}
	ticker := time.NewTicker(24 * time.Hour)
	go func() {
		prune := func() {
			res, err := db.ExecContext(ctx, "DELETE FROM events WHERE created_at < NOW() - ($1 || ' days')::INTERVAL", strconv.Itoa(retentionDays))
			if err != nil {
				slog.Error("Failed to prune old events", "error", err)
			} else {
				rows, _ := res.RowsAffected()
				slog.Info("Pruned expired events", "deleted_rows", rows, "retention_days", retentionDays)
			}
		}
		prune()
		for {
			select {
			case <-ticker.C:
				prune()
			case <-ctx.Done():
				ticker.Stop()
				return
			}
		}
	}()
}

func updateGaugeMetrics() {
	if db == nil {
		return
	}
	var last24h int
	err := db.QueryRow("SELECT COUNT(*) FROM events WHERE created_at >= NOW() - INTERVAL '24 hours'").Scan(&last24h)
	if err == nil {
		attentionEventsLast24h.Set(float64(last24h))
	}
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	var err error
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"
	}
	db, err = sql.Open("pgx", dbURL)
	if err != nil {
		slog.Error("Failed to open database", "error", err)
		os.Exit(1)
	}

	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(10 * time.Minute)

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS events (
		id SERIAL PRIMARY KEY,
		event_type VARCHAR(64) NOT NULL,
		reason TEXT,
		payload JSONB NOT NULL DEFAULT '{}',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`)
	if err != nil {
		slog.Error("Could not create events table", "error", err)
		os.Exit(1)
	}

	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS events_type_idx ON events (event_type)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS events_created_at_idx ON events (created_at DESC)`)

	retentionDays := 90
	if envR := os.Getenv("RETENTION_DAYS"); envR != "" {
		if d, err := strconv.Atoi(envR); err == nil && d > 0 {
			retentionDays = d
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startRetentionWorker(ctx, retentionDays)

	mux := http.NewServeMux()

	mux.Handle("/metrics", promhttp.Handler())

	mux.HandleFunc("/api/metrics", metricsMiddleware("/api/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		ip := getClientIP(r)
		if !limiter.Allow(ip) {
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]string{"error": "Rate limit exceeded"})
			return
		}

		if r.Method == http.MethodPost {
			if !checkAuth(r) {
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "Unauthorized"})
				return
			}

			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

			var req IngestRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}

			var payloadObj map[string]interface{}
			var reason string

			if len(req.Payload) > 0 && req.Payload[0] == '"' {
				var s string
				if err := json.Unmarshal(req.Payload, &s); err == nil {
					_ = json.Unmarshal([]byte(s), &payloadObj)
				}
			} else if len(req.Payload) > 0 {
				_ = json.Unmarshal(req.Payload, &payloadObj)
			}

			if payloadObj != nil {
				if rStr, ok := payloadObj["reason"].(string); ok {
					reason = rStr
				}
			}

			payloadBytes := req.Payload
			if len(payloadBytes) == 0 {
				payloadBytes = []byte("{}")
			} else if len(req.Payload) > 0 && req.Payload[0] == '"' {
				var s string
				if err := json.Unmarshal(req.Payload, &s); err == nil {
					payloadBytes = []byte(s)
				}
			}

			toolName := extractToolName(payloadObj)

			var id int
			err = db.QueryRow("INSERT INTO events (event_type, reason, payload) VALUES ($1, $2, $3) RETURNING id",
				req.EventType, reason, payloadBytes).Scan(&id)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}

			attentionEventsTotal.WithLabelValues(req.EventType, toolName).Inc()

			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{"id": id, "status": "stored"})
		} else if r.Method == http.MethodGet {
			limit := 100
			if l := r.URL.Query().Get("limit"); l != "" {
				if v, err := strconv.Atoi(l); err == nil {
					if v > 500 {
						limit = 500
					} else if v > 0 {
						limit = v
					}
				}
			}

			eventType := r.URL.Query().Get("event_type")

			var rows *sql.Rows
			var err error
			if eventType != "" {
				rows, err = db.Query("SELECT id, event_type, COALESCE(reason, ''), payload, created_at FROM events WHERE event_type = $1 ORDER BY created_at DESC LIMIT $2", eventType, limit)
			} else {
				rows, err = db.Query("SELECT id, event_type, COALESCE(reason, ''), payload, created_at FROM events ORDER BY created_at DESC LIMIT $1", limit)
			}

			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			defer rows.Close()

			events := []Event{}
			for rows.Next() {
				var e Event
				var reason sql.NullString
				if err := rows.Scan(&e.ID, &e.EventType, &reason, &e.Payload, &e.CreatedAt); err != nil {
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
					return
				}
				if reason.Valid {
					e.Reason = reason.String
				}
				events = append(events, e)
			}
			json.NewEncoder(w).Encode(events)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}))

	mux.HandleFunc("/api/metrics/summary", metricsMiddleware("/api/metrics/summary", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var summary SummaryResponse
		err := db.QueryRow(`
			SELECT 
				COUNT(*) as total_events,
				COALESCE(SUM(CASE WHEN event_type = 'PRIMARY_TOOL_DENIED' THEN 1 ELSE 0 END), 0) as denials,
				COALESCE(SUM(CASE WHEN event_type = 'STOP_REQUESTED' THEN 1 ELSE 0 END), 0) as stop_rejections,
				COALESCE(SUM(CASE WHEN created_at >= NOW() - INTERVAL '24 hours' THEN 1 ELSE 0 END), 0) as last_24h
			FROM events
		`).Scan(&summary.TotalEvents, &summary.Denials, &summary.StopRejections, &summary.Last24h)

		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		attentionEventsLast24h.Set(float64(summary.Last24h))
		json.NewEncoder(w).Encode(summary)
	}))

	mux.HandleFunc("/api/metrics/top-tools", metricsMiddleware("/api/metrics/top-tools", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		rows, err := db.Query(`
			SELECT 
				COALESCE(payload->'toolCall'->>'name', payload->>'tool_name', payload->>'tool', 'none') as tool,
				COUNT(*) as count
			FROM events
			WHERE event_type = 'PRIMARY_TOOL_DENIED'
			GROUP BY tool
			ORDER BY count DESC
			LIMIT 5
		`)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()

		tools := []TopTool{}
		for rows.Next() {
			var t TopTool
			if err := rows.Scan(&t.Tool, &t.Count); err == nil {
				tools = append(tools, t)
			}
		}
		json.NewEncoder(w).Encode(tools)
	}))

	mux.HandleFunc("/api/metrics/timeseries", metricsMiddleware("/api/metrics/timeseries", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		rangeDays := 7
		if rng := r.URL.Query().Get("range"); rng == "30d" {
			rangeDays = 30
		} else if rng == "24h" || rng == "1d" {
			rangeDays = 1
		}

		interval := "hour"
		if rangeDays > 7 {
			interval = "day"
		}
		if qInterval := r.URL.Query().Get("interval"); qInterval == "day" || qInterval == "1d" {
			interval = "day"
		} else if qInterval == "hour" || qInterval == "1h" {
			interval = "hour"
		}

		query := fmt.Sprintf(`
			SELECT 
				date_trunc('%s', created_at) as bucket,
				COUNT(*) as total,
				COALESCE(SUM(CASE WHEN event_type = 'PRIMARY_TOOL_DENIED' THEN 1 ELSE 0 END), 0) as denials,
				COALESCE(SUM(CASE WHEN event_type = 'STOP_REQUESTED' THEN 1 ELSE 0 END), 0) as stop_rejections
			FROM events
			WHERE created_at >= NOW() - ($1 || ' days')::INTERVAL
			GROUP BY bucket
			ORDER BY bucket ASC
		`, interval)

		rows, err := db.Query(query, strconv.Itoa(rangeDays))
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()

		points := []TimeseriesPoint{}
		for rows.Next() {
			var p TimeseriesPoint
			if err := rows.Scan(&p.Timestamp, &p.Total, &p.Denials, &p.StopRejections); err == nil {
				points = append(points, p)
			}
		}
		json.NewEncoder(w).Encode(points)
	}))

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := db.Ping(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"status": "error", "database": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{
			"status":     "ok",
			"database":   "connected",
			"version":    Version,
			"go_version": runtime.Version(),
		})
	})

	distPath := "../frontend/dist"
	if _, err := os.Stat(distPath); os.IsNotExist(err) {
		distPath = "./frontend/dist"
	}
	fs := http.FileServer(http.Dir(distPath))
	mux.Handle("/", fs)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8086"
	}

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		slog.Info("Starting attention-metrics-server", "port", port, "version", Version, "go_version", runtime.Version())
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("Server error", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
	<-stop

	slog.Info("Shutting down attention-metrics-server gracefully...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("Server forced to shutdown", "error", err)
	} else {
		slog.Info("Server exited cleanly")
	}
}
