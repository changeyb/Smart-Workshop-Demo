package main

import (
	"encoding/base64"
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

func TestPersonHistoryIncludesUnknownSegmentsAndSupportsNewKeysAndFilters(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "history-unknown.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := &Server{db: db, snapDir: t.TempDir()}

	base := time.Date(2026, 9, 4, 8, 0, 0, 0, time.FixedZone("SGT", 8*60*60))
	insert := func(id, typ string, offset time.Duration, camera, area, track, status, identity, behavior string) {
		t.Helper()
		_, err := db.Exec(`INSERT INTO events(event_id,event_type,occur_time,camera_id,area_id,track_id,
			identity_status,identity_id,behavior,received_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			id, typ, base.Add(offset).Format(time.RFC3339), camera, area, track, status, identity, behavior, nowISO())
		if err != nil {
			t.Fatal(err)
		}
	}
	// 同一观测分段由待识别转为陌生人，应保留为一条陌生人历史。
	unknownFirstEventID := "d142843e-6303-427b-af61-4b923936a905"
	insert(unknownFirstEventID, "PERSON_ENTER", 0, "CAM_001", "ENTRY", "TRACK-UNKNOWN-1", "UNRESOLVED", "", "")
	insert("unknown-status", "IDENTITY_UPDATE", time.Minute, "CAM_001", "ENTRY", "TRACK-UNKNOWN-1", "STRANGER", "", "")
	insert("unknown-alert", "BEHAVIOR", 2*time.Minute, "CAM_001", "BAY_A", "TRACK-UNKNOWN-1", "STRANGER", "", "DANGER_ZONE")
	insert("unknown-leave", "PERSON_LEAVE", 3*time.Minute, "CAM_001", "BAY_A", "TRACK-UNKNOWN-1", "STRANGER", "", "")
	// 另一个陌生人分段不得与上一分段合并。
	insert("stranger-second", "PERSON_ENTER", 10*time.Minute, "CAM_002", "ENTRY", "TRACK-UNKNOWN-2", "STRANGER", "", "")
	insert("stranger-leave", "PERSON_LEAVE", 11*time.Minute, "CAM_002", "ENTRY", "TRACK-UNKNOWN-2", "STRANGER", "", "")
	// 没有离场的待识别分段仍需可查，并标记为不完整。
	insert("unresolved-first", "PERSON_ENTER", 15*time.Minute, "CAM_004", "ENTRY", "TRACK-PENDING", "UNRESOLVED", "", "")
	// 进场时待识别，后续识别成功时整段应回填到员工。
	insert("identified-first", "PERSON_ENTER", 20*time.Minute, "CAM_003", "ENTRY", "TRACK-KNOWN", "UNRESOLVED", "", "")
	insert("identified-update", "IDENTITY_UPDATE", 21*time.Minute, "CAM_003", "ENTRY", "TRACK-KNOWN", "IDENTIFIED", "EMP_10086", "")
	insert("identified-leave", "PERSON_LEAVE", 22*time.Minute, "CAM_003", "ENTRY", "TRACK-KNOWN", "IDENTIFIED", "EMP_10086", "")

	from := url.QueryEscape(base.Add(-time.Minute).Format(time.RFC3339))
	to := url.QueryEscape(base.Add(time.Hour).Format(time.RFC3339))
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
	if len(list) != 4 {
		t.Fatalf("want four independent people, got %d: %#v", len(list), list)
	}
	unknownKey := "obs_" + base64.RawURLEncoding.EncodeToString([]byte(unknownFirstEventID))
	identifiedKey := "id_" + base64.RawURLEncoding.EncodeToString([]byte("EMP_10086"))
	seen := map[string]map[string]any{}
	for _, raw := range list {
		item := raw.(map[string]any)
		seen[item["person_key"].(string)] = item
	}
	if seen[unknownKey]["identity_status"] != "STRANGER" || seen[unknownKey]["segment_count"] != float64(1) {
		t.Fatalf("first stranger summary: %#v", seen[unknownKey])
	}
	if seen[identifiedKey]["identity_status"] != "IDENTIFIED" || seen[identifiedKey]["event_count"] != float64(3) {
		t.Fatalf("backfilled identity summary: %#v", seen[identifiedKey])
	}
	unresolvedKey := "obs_" + base64.RawURLEncoding.EncodeToString([]byte("unresolved-first"))
	if seen[unresolvedKey]["identity_status"] != "UNRESOLVED" || seen[unresolvedKey]["incomplete_segments"] != float64(1) {
		t.Fatalf("unresolved incomplete summary: %#v", seen[unresolvedKey])
	}
	tracks := seen[unknownKey]["track_ids"].([]any)
	if len(tracks) != 1 || tracks[0] != "TRACK-UNKNOWN-1" {
		t.Fatalf("track ids: %#v", tracks)
	}

	code, data = request("/api/v1/history/person-visits?from=" + from + "&to=" + to + "&identity_status=STRANGER&keyword=track-unknown-1&camera_id=CAM_001")
	if code != 0 || len(data["list"].([]any)) != 1 || data["list"].([]any)[0].(map[string]any)["person_key"] != unknownKey {
		t.Fatalf("combined status/keyword/camera filter: code=%d data=%#v", code, data)
	}
	for _, keyword := range []string{"tan wei", "10086", "emp_10086", "track-known"} {
		code, data = request("/api/v1/history/person-visits?from=" + from + "&to=" + to + "&keyword=" + url.QueryEscape(keyword))
		if code != 0 || len(data["list"].([]any)) != 1 || data["list"].([]any)[0].(map[string]any)["person_key"] != identifiedKey {
			t.Fatalf("keyword %q: code=%d data=%#v", keyword, code, data)
		}
	}

	code, data = request("/api/v1/history/person-visits/" + unknownKey + "?from=" + from + "&to=" + to)
	if code != 0 || len(data["nodes"].([]any)) != 4 {
		t.Fatalf("unknown detail by person key: code=%d data=%#v", code, data)
	}
	code, data = request("/api/v1/history/person-visits/" + identifiedKey + "?from=" + from + "&to=" + to)
	if code != 0 || len(data["nodes"].([]any)) != 3 {
		t.Fatalf("identified detail by person key: code=%d data=%#v", code, data)
	}
	code, data = request("/api/v1/history/person-visits/EMP_10086?from=" + from + "&to=" + to)
	if code != 0 || len(data["nodes"].([]any)) != 3 {
		t.Fatalf("legacy identity detail: code=%d data=%#v", code, data)
	}
}

func TestPersonHistoryRejectsInvalidIdentityStatus(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "history-status.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := &Server{db: db, snapDir: t.TempDir()}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/api/v1/history/person-visits?identity_status=UNKNOWN", nil))
	var response struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Code != 40001 {
		t.Fatalf("invalid identity status response: %v, %s", err, w.Body.String())
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
