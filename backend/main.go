package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

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

var db *sql.DB

func main() {
	var err error
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"
	}
	db, err = sql.Open("pgx", dbURL)
	if err != nil {
		log.Fatal(err)
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS events (
		id SERIAL PRIMARY KEY,
		event_type VARCHAR(64) NOT NULL,
		reason TEXT,
		payload JSONB NOT NULL DEFAULT '{}',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`)
	if err != nil {
		log.Fatal("Could not create table: ", err)
	}

	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS events_type_idx ON events (event_type)`)
	if err != nil {
		log.Fatal("Could not create index events_type_idx: ", err)
	}

	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS events_created_at_idx ON events (created_at DESC)`)
	if err != nil {
		log.Fatal("Could not create index events_created_at_idx: ", err)
	}

	http.HandleFunc("/api/metrics", func(w http.ResponseWriter, r *http.Request) {
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
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer rows.Close()

			events := []Event{}
			for rows.Next() {
				var e Event
				var reason sql.NullString
				if err := rows.Scan(&e.ID, &e.EventType, &reason, &e.Payload, &e.CreatedAt); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
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
	})

	http.HandleFunc("/api/metrics/summary", func(w http.ResponseWriter, r *http.Request) {
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
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		json.NewEncoder(w).Encode(summary)
	})

	http.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := db.Ping(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "database": "connected"})
	})

	// Serve static files
	distPath := "../frontend/dist"
	if _, err := os.Stat(distPath); os.IsNotExist(err) {
		distPath = "./frontend/dist"
	}
	fs := http.FileServer(http.Dir(distPath))
	http.Handle("/", fs)

	log.Println("Listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
