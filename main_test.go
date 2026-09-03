package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoutesWithoutAuthentication(t *testing.T) {
	t.Setenv("WS_TOKEN", "legacy-token-must-be-ignored")
	db, err := openDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := &Server{db: db, snapDir: t.TempDir()}
	h := cors(s.routes())
	request := func(method, path, body string, wantCode int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: HTTP %d: %s", method, path, w.Code, w.Body.String())
		}
		var response struct {
			Code *int           `json:"code"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Code == nil || *response.Code != wantCode {
			t.Fatalf("%s %s: %s", method, path, w.Body.String())
		}
		return response.Data
	}

	request("GET", "/api/v1/dashboard", "", 0)
	request("GET", "/api/v1/events", "", 0)
	request("POST", "/api/v1/heartbeat", `{"device_id":"test","time":"`+nowISO()+`","cameras":[{"camera_id":"CAM_001","status":"ONLINE","fps":12}]}`, 0)
	event := `[{"event_id":"no-auth-event","event_type":"SPOT_CHANGE","occur_time":"` + nowISO() + `","camera_id":"CAM_001","spot":{"spot_id":"A-01","status":"OCCUPIED"}}]`
	if data := request("POST", "/api/v1/events", event, 0); data["accepted"] != float64(1) {
		t.Fatalf("event not accepted: %v", data)
	}
	if data := request("POST", "/api/v1/events", event, 0); data["duplicated"] != float64(1) {
		t.Fatalf("event not deduplicated: %v", data)
	}
	request("POST", "/api/v1/spots/A-01/override", `{"status":"FREE","operator":"test"}`, 0)
	request("POST", "/api/v1/spots/A-01/override", `{"status":"INVALID"}`, 40001)
	var status string
	if err := db.QueryRow("SELECT status FROM parking_spots WHERE spot_id='A-01'").Scan(&status); err != nil || status != "FREE" {
		t.Fatalf("override state: %q, %v", status, err)
	}
	// 旧客户端携带失效令牌仍可访问，不再触发鉴权。
	r := httptest.NewRequest("GET", "/api/v1/dashboard", nil)
	r.Header.Set("Authorization", "Bearer stale-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("legacy authorization header: HTTP %d", w.Code)
	}
}
