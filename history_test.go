package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"
)

func TestPersonHistoryGroupsTracksAndBackfillsIdentity(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := &Server{db: db, snapDir: t.TempDir()}

	base := time.Date(2026, 9, 4, 8, 0, 0, 0, time.FixedZone("SGT", 8*60*60))
	insert := func(id, typ string, offset time.Duration, camera, area, track, identity, behavior string) {
		t.Helper()
		_, err := db.Exec(`INSERT INTO events(event_id,event_type,occur_time,camera_id,area_id,track_id,
			identity_id,behavior,received_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			id, typ, base.Add(offset).Format(time.RFC3339), camera, area, track, identity, behavior, nowISO())
		if err != nil {
			t.Fatal(err)
		}
	}
	// CAM_001 上先进场、后识别；识别前的进场节点应归入同一员工。
	insert("h-1", "PERSON_ENTER", 0, "CAM_001", "ENTRY", "T-1", "", "")
	insert("h-2", "IDENTITY_UPDATE", 5*time.Minute, "CAM_001", "ENTRY", "T-1", "EMP_10086", "")
	insert("h-3", "BEHAVIOR", 10*time.Minute, "CAM_001", "BAY_A", "T-1", "", "NO_HELMET")
	insert("h-4", "PERSON_LEAVE", 30*time.Minute, "CAM_001", "BAY_A", "T-1", "", "")
	// 另一摄像头使用不同 track_id，只按同一身份汇总，不推断两段之间的路线。
	insert("h-5", "PERSON_ENTER", 40*time.Minute, "CAM_002", "BAY_B", "T-9", "EMP_10086", "")
	insert("h-6", "PERSON_LEAVE", 55*time.Minute, "CAM_002", "BAY_B", "T-9", "", "")
	insert("h-7", "PERSON_ENTER", 60*time.Minute, "CAM_001", "ENTRY", "T-2", "EMP_10023", "")
	insert("h-8", "PERSON_LEAVE", 70*time.Minute, "CAM_001", "ENTRY", "T-2", "", "")

	from := url.QueryEscape(base.Add(-time.Minute).Format(time.RFC3339))
	to := url.QueryEscape(base.Add(2 * time.Hour).Format(time.RFC3339))
	h := s.routes()
	request := func(path string) (int, map[string]any) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		var response struct {
			Code int            `json:"code"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode %s: %v: %s", path, err, w.Body.String())
		}
		return response.Code, response.Data
	}

	code, data := request("/api/v1/history/person-visits?from=" + from + "&to=" + to)
	if code != 0 {
		t.Fatalf("list code: %d", code)
	}
	list := data["list"].([]any)
	if len(list) != 2 {
		t.Fatalf("list size: %d, %#v", len(list), list)
	}
	first := list[0].(map[string]any)
	if first["identity_id"] != "EMP_10023" {
		// 结果按最后出现倒序，EMP_10023 在 09:10 最后出现。
		t.Fatalf("first identity: %#v", first)
	}
	var employee map[string]any
	for _, raw := range list {
		item := raw.(map[string]any)
		if item["identity_id"] == "EMP_10086" {
			employee = item
		}
	}
	if employee == nil || employee["event_count"] != float64(6) || employee["segment_count"] != float64(2) || employee["alert_count"] != float64(1) {
		t.Fatalf("employee summary: %#v", employee)
	}

	code, data = request("/api/v1/history/person-visits/EMP_10086?from=" + from + "&to=" + to)
	if code != 0 || len(data["nodes"].([]any)) != 6 {
		t.Fatalf("detail: code=%d data=%#v", code, data)
	}
	nodes := data["nodes"].([]any)
	if nodes[0].(map[string]any)["event_id"] != "h-1" {
		t.Fatalf("identity backfill omitted entry: %#v", nodes)
	}

	code, data = request("/api/v1/history/person-visits?from=" + from + "&to=" + to + "&area_id=BAY_B")
	if code != 0 || len(data["list"].([]any)) != 1 {
		t.Fatalf("area filter: code=%d data=%#v", code, data)
	}
	areaEmployee := data["list"].([]any)[0].(map[string]any)
	if areaEmployee["event_count"] != float64(2) || areaEmployee["identity_id"] != "EMP_10086" {
		t.Fatalf("area summary: %#v", areaEmployee)
	}

	code, data = request("/api/v1/history/person-visits?from=" + from + "&to=" + to + "&alert_only=1")
	if code != 0 || len(data["list"].([]any)) != 1 || data["list"].([]any)[0].(map[string]any)["identity_id"] != "EMP_10086" {
		t.Fatalf("alert filter: code=%d data=%#v", code, data)
	}
}

func TestPersonHistoryRejectsInvalidRange(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := &Server{db: db, snapDir: t.TempDir()}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/api/v1/history/person-visits?from=2026-01-01T00:00:00Z&to=2026-03-01T00:00:00Z", nil))
	var response struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Code != 40001 {
		t.Fatalf("invalid range response: %v, %s", err, w.Body.String())
	}
}
