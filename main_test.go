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
	var spotCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM parking_spots").Scan(&spotCount); err != nil || spotCount != 6 {
		t.Fatalf("spot count: %d, %v", spotCount, err)
	}
	request("GET", "/api/v1/events", "", 0)
	request("POST", "/api/v1/heartbeat", `{"device_id":"test","time":"`+nowISO()+`","cameras":[{"camera_id":"CAM_001","status":"ONLINE","fps":12}]}`, 0)
	event := `[{"event_id":"no-auth-event","event_type":"SPOT_CHANGE","occur_time":"` + nowISO() + `","camera_id":"CAM_001","spot":{"spot_id":"A-01","status":"OCCUPIED"}}]`
	if data := request("POST", "/api/v1/events", event, 0); data["accepted"] != float64(1) {
		t.Fatalf("event not accepted: %v", data)
	}
	if data := request("POST", "/api/v1/events", event, 0); data["duplicated"] != float64(1) {
		t.Fatalf("event not deduplicated: %v", data)
	}
	request("POST", "/api/v1/spots/A-01/override", `{"status":"FREE","reason_code":"VEHICLE_LEFT","operator":"test"}`, 0)
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

func TestOverrideValidationAndAuditEvent(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := &Server{db: db, snapDir: t.TempDir()}
	h := cors(s.routes())
	request := func(body string, wantCode int) {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/v1/spots/A-02/override", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
		}
		var response struct {
			Code *int `json:"code"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Code == nil || *response.Code != wantCode {
			t.Fatalf("body %s: response %s", body, w.Body.String())
		}
	}

	request(`{"status":"FREE"}`, 40001)
	request(`{"status":"FREE","reason_code":"VEHICLE_ARRIVED"}`, 40001)
	request(`{"status":"BLOCKED","reason_code":"OTHER"}`, 40001)
	request(`{"status":"OCCUPIED","reason_code":"OTHER","remark":"`+strings.Repeat("车", 201)+`"}`, 40001)
	request(`{"status":"BLOCKED","reason_code":"BAY_MAINTENANCE","operator":"dashboard","remark":"升降机检修"}`, 0)

	dashboardRequest := httptest.NewRequest("GET", "/api/v1/dashboard", nil)
	dashboardRecorder := httptest.NewRecorder()
	h.ServeHTTP(dashboardRecorder, dashboardRequest)
	var dashboardResponse struct {
		Code int `json:"code"`
		Data struct {
			Spots []struct {
				SpotID    string `json:"spot_id"`
				UpdatedAt string `json:"updated_at"`
			} `json:"spots"`
		} `json:"data"`
	}
	if err := json.Unmarshal(dashboardRecorder.Body.Bytes(), &dashboardResponse); err != nil {
		t.Fatal(err)
	}
	if dashboardResponse.Code != 0 {
		t.Fatalf("dashboard response: %s", dashboardRecorder.Body.String())
	}
	updatedAt := ""
	for _, spot := range dashboardResponse.Data.Spots {
		if spot.SpotID == "A-02" {
			updatedAt = spot.UpdatedAt
			break
		}
	}
	if updatedAt == "" {
		t.Fatalf("dashboard spot A-02 missing updated_at: %s", dashboardRecorder.Body.String())
	}

	var status string
	if err := db.QueryRow("SELECT status FROM parking_spots WHERE spot_id='A-02'").Scan(&status); err != nil || status != "BLOCKED" {
		t.Fatalf("override state: %q, %v", status, err)
	}
	var rawJSON string
	if err := db.QueryRow(`SELECT raw_json FROM events WHERE event_type='OPERATOR_OVERRIDE' AND spot_id='A-02' ORDER BY received_at DESC LIMIT 1`).Scan(&rawJSON); err != nil {
		t.Fatal(err)
	}
	var audit struct {
		Status     string `json:"status"`
		ReasonCode string `json:"reason_code"`
		Operator   string `json:"operator"`
		Remark     string `json:"remark"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &audit); err != nil {
		t.Fatalf("raw_json %q: %v", rawJSON, err)
	}
	if audit.Status != "BLOCKED" || audit.ReasonCode != "BAY_MAINTENANCE" || audit.Operator != "dashboard" || audit.Remark != "升降机检修" {
		t.Fatalf("unexpected audit payload: %+v", audit)
	}
}
