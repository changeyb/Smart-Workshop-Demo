package main

import (
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"
)

//go:embed static
var staticFS embed.FS

type Server struct {
	db        *sql.DB
	snapDir   string
	startTime time.Time
}

func main() {
	addr := envOr("WS_ADDR", ":8080")
	dbPath := envOr("WS_DB", "data/workshop.db")
	snapDir := envOr("WS_SNAPSHOT_DIR", "data/snapshots")

	db, err := openDB(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		log.Fatal(err)
	}

	s := &Server{db: db, snapDir: snapDir, startTime: time.Now()}
	log.Printf("workshop-server listening on %s (authentication disabled; trusted network only)", addr)
	log.Fatal(http.ListenAndServe(addr, cors(s.routes())))
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/events", s.handleEvents)
	mux.HandleFunc("POST /api/v1/heartbeat", s.handleHeartbeat)
	mux.HandleFunc("GET /api/v1/dashboard", s.handleDashboard)
	mux.HandleFunc("GET /api/v1/events", s.handleQueryEvents)
	mux.HandleFunc("GET /api/v1/history/person-visits", s.handlePersonHistory)
	mux.HandleFunc("GET /api/v1/history/person-visits/{key}", s.handlePersonHistoryDetail)
	mux.HandleFunc("POST /api/v1/spots/{spot_id}/override", s.handleOverride)
	mux.Handle("GET /snapshots/", http.StripPrefix("/snapshots/", http.FileServer(http.Dir(s.snapDir))))

	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /", http.FileServerFS(sub))

	return mux
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func ok(data any) map[string]any {
	return map[string]any{"code": 0, "message": "ok", "data": data}
}

func fail(code int, msg string) map[string]any {
	return map[string]any{"code": code, "message": msg}
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func nowISO() string {
	return time.Now().Format("2006-01-02T15:04:05.000-07:00")
}
