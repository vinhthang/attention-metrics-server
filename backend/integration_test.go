//go:build integration

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestIntegration(t *testing.T) {
	ctx := context.Background()
	var dbURL string

	if overrideURL := os.Getenv("TEST_DATABASE_URL"); overrideURL != "" {
		dbURL = overrideURL
	} else {
		pgContainer, err := postgres.RunContainer(ctx,
			testcontainers.WithImage("postgres:16-alpine"),
			postgres.WithDatabase("testdb"),
			postgres.WithUsername("testuser"),
			postgres.WithPassword("testpass"),
			testcontainers.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).WithStartupTimeout(60*time.Second)),
		)
		if err != nil {
			t.Fatalf("failed to start container: %s", err)
		}
		defer func() {
			if err := pgContainer.Terminate(ctx); err != nil {
				t.Fatalf("failed to terminate container: %s", err)
			}
		}()

		connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			t.Fatalf("failed to get connection string: %s", err)
		}
		dbURL = connStr
	}

	os.Setenv("DATABASE_URL", dbURL)

	var err error
	db, err = sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatalf("failed to connect to db: %s", err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS events (
		id SERIAL PRIMARY KEY,
		event_type VARCHAR(64) NOT NULL,
		reason TEXT,
		payload JSONB NOT NULL DEFAULT '{}',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`)
	if err != nil {
		t.Fatalf("failed to create table: %s", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	mux.HandleFunc("/api/metrics", func(w http.ResponseWriter, r *http.Request) {
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
			rows, err := db.Query("SELECT id, event_type, COALESCE(reason, ''), payload, created_at FROM events ORDER BY created_at DESC LIMIT 100")
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
		}
	})

	mux.HandleFunc("/api/metrics/summary", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
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
	})

	mux.HandleFunc("/api/metrics/top-tools", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
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
	})

	mux.HandleFunc("/api/metrics/timeseries", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		rows, err := db.Query(`
			SELECT 
				date_trunc('hour', created_at) as bucket,
				COUNT(*) as total,
				COALESCE(SUM(CASE WHEN event_type = 'PRIMARY_TOOL_DENIED' THEN 1 ELSE 0 END), 0) as denials,
				COALESCE(SUM(CASE WHEN event_type = 'STOP_REQUESTED' THEN 1 ELSE 0 END), 0) as stop_rejections
			FROM events
			WHERE created_at >= NOW() - INTERVAL '7 days'
			GROUP BY bucket
			ORDER BY bucket ASC
		`)
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
	})

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := db.Ping(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"status": "error"})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{
			"status":     "ok",
			"database":   "connected",
			"version":    Version,
			"go_version": "go1.27.0",
		})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	// 1. Ingest Event with toolCall
	payload1 := `{"event_type": "PRIMARY_TOOL_DENIED", "payload": {"toolCall": {"name": "write_to_file"}, "reason": "Direct edit blocked"}}`
	resp1, err := http.Post(server.URL+"/api/metrics", "application/json", bytes.NewBufferString(payload1))
	if err != nil {
		t.Fatalf("POST 1 failed: %v", err)
	}
	if resp1.StatusCode != http.StatusCreated {
		t.Errorf("Expected status 201, got %d", resp1.StatusCode)
	}

	// 2. Ingest Second Event
	payload2 := `{"event_type": "STOP_REQUESTED", "payload": {"retries_exhausted": true}}`
	resp2, err := http.Post(server.URL+"/api/metrics", "application/json", bytes.NewBufferString(payload2))
	if err != nil {
		t.Fatalf("POST 2 failed: %v", err)
	}
	if resp2.StatusCode != http.StatusCreated {
		t.Errorf("Expected status 201, got %d", resp2.StatusCode)
	}

	// 3. Test GET /api/metrics
	respGet, err := http.Get(server.URL + "/api/metrics")
	if err != nil {
		t.Fatalf("GET /api/metrics failed: %v", err)
	}
	defer respGet.Body.Close()
	var events []Event
	if err := json.NewDecoder(respGet.Body).Decode(&events); err != nil {
		t.Fatalf("Failed to decode GET /api/metrics response: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("Expected 2 events, got %d", len(events))
	}

	// 4. Test GET /api/metrics/summary
	respSum, err := http.Get(server.URL + "/api/metrics/summary")
	if err != nil {
		t.Fatalf("GET /api/metrics/summary failed: %v", err)
	}
	defer respSum.Body.Close()
	var summary SummaryResponse
	if err := json.NewDecoder(respSum.Body).Decode(&summary); err != nil {
		t.Fatalf("Failed to decode summary response: %v", err)
	}
	if summary.TotalEvents != 2 || summary.Denials != 1 || summary.StopRejections != 1 {
		t.Errorf("Unexpected summary values: %+v", summary)
	}

	// 5. Test Prometheus /metrics exporter
	respMetrics, err := http.Get(server.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics failed: %v", err)
	}
	defer respMetrics.Body.Close()
	metricsText, _ := io.ReadAll(respMetrics.Body)
	if !strings.Contains(string(metricsText), "attention_events_total") {
		t.Errorf("Expected /metrics to contain attention_events_total metric")
	}

	// 6. Test GET /api/metrics/top-tools
	respTools, err := http.Get(server.URL + "/api/metrics/top-tools")
	if err != nil {
		t.Fatalf("GET /api/metrics/top-tools failed: %v", err)
	}
	defer respTools.Body.Close()
	var tools []TopTool
	if err := json.NewDecoder(respTools.Body).Decode(&tools); err != nil {
		t.Fatalf("Failed to decode top-tools: %v", err)
	}
	if len(tools) == 0 || tools[0].Tool != "write_to_file" {
		t.Errorf("Expected write_to_file as top tool, got %+v", tools)
	}

	// 7. Test GET /api/metrics/timeseries
	respTS, err := http.Get(server.URL + "/api/metrics/timeseries")
	if err != nil {
		t.Fatalf("GET /api/metrics/timeseries failed: %v", err)
	}
	defer respTS.Body.Close()
	var tsPoints []TimeseriesPoint
	if err := json.NewDecoder(respTS.Body).Decode(&tsPoints); err != nil {
		t.Fatalf("Failed to decode timeseries: %v", err)
	}
	if len(tsPoints) == 0 {
		t.Errorf("Expected at least one timeseries bucket, got 0")
	}

	// 8. Test GET /api/health
	respHealth, err := http.Get(server.URL + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health failed: %v", err)
	}
	defer respHealth.Body.Close()
	var health map[string]string
	if err := json.NewDecoder(respHealth.Body).Decode(&health); err != nil {
		t.Fatalf("Failed to decode health response: %v", err)
	}
	if health["version"] != Version {
		t.Errorf("Expected version %s, got %s", Version, health["version"])
	}

	// 9. Test API Key Auth
	os.Setenv("METRICS_API_KEY", "secret-test-key")
	defer os.Unsetenv("METRICS_API_KEY")

	// Missing key should be 401
	respUnauthorized, err := http.Post(server.URL+"/api/metrics", "application/json", bytes.NewBufferString(payload1))
	if err != nil || respUnauthorized.StatusCode != http.StatusUnauthorized {
		t.Errorf("Expected 401 Unauthorized for missing API key, got %v", respUnauthorized.StatusCode)
	}

	// Valid key should be 201
	reqAuth, _ := http.NewRequest("POST", server.URL+"/api/metrics", bytes.NewBufferString(payload1))
	reqAuth.Header.Set("Content-Type", "application/json")
	reqAuth.Header.Set("X-API-Key", "secret-test-key")
	respAuthorized, err := http.DefaultClient.Do(reqAuth)
	if err != nil || respAuthorized.StatusCode != http.StatusCreated {
		t.Errorf("Expected 201 Created with valid API key, got %v", respAuthorized.StatusCode)
	}
}
