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
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
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
	mux.HandleFunc("/api/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			var req IngestRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
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
				if r, ok := payloadObj["reason"].(string); ok {
					reason = r
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

			var id int
			err = db.QueryRow("INSERT INTO events (event_type, reason, payload) VALUES ($1, $2, $3) RETURNING id",
				req.EventType, reason, payloadBytes).Scan(&id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{"id": id, "status": "stored"})
		} else if r.Method == http.MethodGet {
			rows, err := db.Query("SELECT id, event_type, COALESCE(reason, ''), payload FROM events ORDER BY created_at DESC")
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer rows.Close()
			var events []Event
			for rows.Next() {
				var e Event
				var reason string
				if err := rows.Scan(&e.ID, &e.EventType, &reason, &e.Payload); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				e.Reason = reason
				events = append(events, e)
			}
			if events == nil {
				events = []Event{}
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
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(summary)
	})

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := db.Ping(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "database": "connected"})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	payload1 := `{"event_type": "PRIMARY_TOOL_DENIED", "payload": "{\"reason\": \"Forbidden shell command\"}"}`
	resp1, err := http.Post(server.URL+"/api/metrics", "application/json", bytes.NewBufferString(payload1))
	if err != nil {
		t.Fatalf("POST 1 failed: %v", err)
	}
	if resp1.StatusCode != http.StatusCreated {
		t.Errorf("Expected status 201, got %d", resp1.StatusCode)
	}

	payload2 := `{"event_type": "STOP_REQUESTED", "payload": {"retries_exhausted": true}}`
	resp2, err := http.Post(server.URL+"/api/metrics", "application/json", bytes.NewBufferString(payload2))
	if err != nil {
		t.Fatalf("POST 2 failed: %v", err)
	}
	if resp2.StatusCode != http.StatusCreated {
		t.Errorf("Expected status 201, got %d", resp2.StatusCode)
	}

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
	if len(events) == 2 {
		if events[0].EventType != "STOP_REQUESTED" {
			t.Errorf("Expected STOP_REQUESTED, got %s", events[0].EventType)
		}
		if events[1].EventType != "PRIMARY_TOOL_DENIED" {
			t.Errorf("Expected PRIMARY_TOOL_DENIED, got %s", events[1].EventType)
		}
		if events[1].Reason != "Forbidden shell command" {
			t.Errorf("Expected Reason 'Forbidden shell command', got '%s'", events[1].Reason)
		}
	}

	respSum, err := http.Get(server.URL + "/api/metrics/summary")
	if err != nil {
		t.Fatalf("GET /api/metrics/summary failed: %v", err)
	}
	defer respSum.Body.Close()
	var summary SummaryResponse
	if err := json.NewDecoder(respSum.Body).Decode(&summary); err != nil {
		t.Fatalf("Failed to decode summary response: %v", err)
	}
	if summary.TotalEvents != 2 {
		t.Errorf("Expected 2 total events, got %d", summary.TotalEvents)
	}
	if summary.Denials != 1 {
		t.Errorf("Expected 1 denial, got %d", summary.Denials)
	}
	if summary.StopRejections != 1 {
		t.Errorf("Expected 1 stop rejection, got %d", summary.StopRejections)
	}
	if summary.Last24h != 2 {
		t.Errorf("Expected 2 last 24h, got %d", summary.Last24h)
	}

	respHealth, err := http.Get(server.URL + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health failed: %v", err)
	}
	defer respHealth.Body.Close()
	b, _ := io.ReadAll(respHealth.Body)
	if !bytes.Contains(b, []byte("connected")) {
		t.Errorf("Expected health check to return connected, got %s", b)
	}
}
