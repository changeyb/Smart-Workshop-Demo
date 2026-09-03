package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ── 上报报文结构（接口协议 v1.1） ──

type Track struct {
	Type    string `json:"type"`
	TrackID string `json:"track_id"`
}

type Identity struct {
	Status     string  `json:"status"`
	IdentityID string  `json:"identity_id"`
	Confidence float64 `json:"confidence"`
}

type Person struct {
	Identity *Identity `json:"identity"`
	Behavior string    `json:"behavior"`
	Snapshot string    `json:"snapshot"`
}

type Vehicle struct {
	PlateNo    string  `json:"plate_no"`
	Confidence float64 `json:"confidence"`
}

type Spot struct {
	SpotID string `json:"spot_id"`
	Status string `json:"status"`
}

type EventIn struct {
	EventID   string   `json:"event_id"`
	EventType string   `json:"event_type"`
	OccurTime string   `json:"occur_time"`
	CameraID  string   `json:"camera_id"`
	AreaID    string   `json:"area_id"`
	Track     *Track   `json:"track"`
	Person    *Person  `json:"person"`
	Vehicle   *Vehicle `json:"vehicle"`
	Spot      *Spot    `json:"spot"`
}

var validTypes = map[string]bool{
	"PERSON_ENTER": true, "PERSON_LEAVE": true, "IDENTITY_UPDATE": true,
	"BEHAVIOR": true, "VEHICLE_IN": true, "VEHICLE_OUT": true,
	"SPOT_CHANGE": true, "OPERATOR_OVERRIDE": true,
}

var validSpotStatus = map[string]bool{"FREE": true, "OCCUPIED": true, "BLOCKED": true}

// validate 返回错误原因，空串表示通过
func (e *EventIn) validate() string {
	if e.EventID == "" {
		return "missing event_id"
	}
	if !validTypes[e.EventType] {
		return "invalid event_type"
	}
	if e.OccurTime == "" {
		return "missing occur_time"
	}
	if _, err := time.Parse(time.RFC3339Nano, e.OccurTime); err != nil {
		return "occur_time not RFC3339"
	}
	if e.CameraID == "" {
		return "missing camera_id"
	}
	switch e.EventType {
	case "SPOT_CHANGE":
		if e.Spot == nil || e.Spot.SpotID == "" || !validSpotStatus[e.Spot.Status] {
			return "SPOT_CHANGE requires spot.spot_id and valid spot.status"
		}
	case "BEHAVIOR":
		if e.Person == nil || e.Person.Behavior == "" {
			return "BEHAVIOR requires person.behavior"
		}
	case "PERSON_ENTER", "PERSON_LEAVE", "IDENTITY_UPDATE":
		if e.Track == nil || e.Track.TrackID == "" {
			return "person event requires track"
		}
	}
	return ""
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	var batch []EventIn
	if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
		writeJSON(w, http.StatusOK, fail(40001, "invalid json: "+err.Error()))
		return
	}
	if len(batch) == 0 || len(batch) > 100 {
		writeJSON(w, http.StatusOK, fail(40001, "batch size must be 1..100"))
		return
	}

	accepted, duplicated := 0, 0
	type rej struct {
		EventID string `json:"event_id"`
		Reason  string `json:"reason"`
	}
	rejected := []rej{}

	tx, err := s.db.Begin()
	if err != nil {
		writeJSON(w, http.StatusOK, fail(50000, err.Error()))
		return
	}
	defer tx.Rollback()

	for i := range batch {
		e := &batch[i]
		if reason := e.validate(); reason != "" {
			rejected = append(rejected, rej{e.EventID, reason})
			continue
		}

		snapPath := ""
		if e.Person != nil && e.Person.Snapshot != "" {
			snapPath = s.saveSnapshot(e.EventID, e.Person.Snapshot)
		}

		raw, _ := json.Marshal(e)
		// INSERT OR IGNORE：event_id 唯一索引保证幂等，重试/补传安全
		res, err := tx.Exec(`INSERT OR IGNORE INTO events
			(event_id, event_type, occur_time, camera_id, area_id, track_type, track_id,
			 identity_status, identity_id, identity_conf, behavior,
			 plate_no, plate_conf, spot_id, spot_status, snapshot_path, raw_json, received_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			e.EventID, e.EventType, e.OccurTime, e.CameraID, e.AreaID,
			trackType(e), trackID(e), identityStatus(e), identityID(e), identityConf(e),
			behavior(e), plateNo(e), plateConf(e), spotID(e), spotStatus(e),
			snapPath, string(raw), nowISO())
		if err != nil {
			writeJSON(w, http.StatusOK, fail(50000, err.Error()))
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			duplicated++
			continue
		}

		// 副作用：车位状态驱动
		if e.EventType == "SPOT_CHANGE" {
			_, err = tx.Exec(`INSERT INTO parking_spots(spot_id, status, plate_no, since, overridden, updated_at)
				VALUES(?,?,?,?,0,?)
				ON CONFLICT(spot_id) DO UPDATE SET
				  status=excluded.status, plate_no=excluded.plate_no,
				  since=excluded.since, overridden=0, updated_at=excluded.updated_at`,
				e.Spot.SpotID, e.Spot.Status, plateNo(e), e.OccurTime, nowISO())
			if err != nil {
				writeJSON(w, http.StatusOK, fail(50000, err.Error()))
				return
			}
		}
		accepted++
	}

	if err := tx.Commit(); err != nil {
		writeJSON(w, http.StatusOK, fail(50000, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, ok(map[string]any{
		"accepted": accepted, "duplicated": duplicated, "rejected": rejected}))
}

func (s *Server) saveSnapshot(eventID, b64 string) string {
	// 容忍 data:image/jpeg;base64, 前缀
	if i := strings.Index(b64, ","); i >= 0 && i < 40 {
		b64 = b64[i+1:]
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(data) == 0 || len(data) > 2<<20 {
		return ""
	}
	name := eventID + ".jpg"
	if err := os.WriteFile(filepath.Join(s.snapDir, name), data, 0o644); err != nil {
		return ""
	}
	return "/snapshots/" + name
}

// ── 心跳 ──

type HeartbeatIn struct {
	DeviceID string `json:"device_id"`
	Time     string `json:"time"`
	Cameras  []struct {
		CameraID string  `json:"camera_id"`
		Status   string  `json:"status"`
		FPS      float64 `json:"fps"`
	} `json:"cameras"`
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	var hb HeartbeatIn
	if err := json.NewDecoder(r.Body).Decode(&hb); err != nil || hb.DeviceID == "" {
		writeJSON(w, http.StatusOK, fail(40001, "invalid heartbeat"))
		return
	}
	offset := int64(0)
	if t, err := time.Parse(time.RFC3339Nano, hb.Time); err == nil {
		offset = time.Since(t).Milliseconds()
	}
	for _, c := range hb.Cameras {
		if c.CameraID == "" {
			continue
		}
		if _, err := s.db.Exec(`INSERT INTO cameras(camera_id, status, fps, device_id, last_heartbeat, clock_offset_ms)
			VALUES(?,?,?,?,?,?)
			ON CONFLICT(camera_id) DO UPDATE SET
			  status=excluded.status, fps=excluded.fps, device_id=excluded.device_id,
			  last_heartbeat=excluded.last_heartbeat, clock_offset_ms=excluded.clock_offset_ms`,
			c.CameraID, c.Status, c.FPS, hb.DeviceID, nowISO(), offset); err != nil {
			writeJSON(w, http.StatusOK, fail(50000, err.Error()))
			return
		}
	}
	writeJSON(w, http.StatusOK, ok(map[string]any{"received": len(hb.Cameras), "clock_offset_ms": offset}))
}

// ── 人工修正 ──

func (s *Server) handleOverride(w http.ResponseWriter, r *http.Request) {
	spotID := r.PathValue("spot_id")
	var in struct {
		Status   string `json:"status"`
		Operator string `json:"operator"`
		Remark   string `json:"remark"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || !validSpotStatus[in.Status] {
		writeJSON(w, http.StatusOK, fail(40001, "invalid body or status"))
		return
	}
	if in.Operator == "" {
		in.Operator = "admin"
	}
	res, err := s.db.Exec(`UPDATE parking_spots SET status=?, overridden=1, since=?, updated_at=? WHERE spot_id=?`,
		in.Status, nowISO(), nowISO(), spotID)
	if err != nil {
		writeJSON(w, http.StatusOK, fail(50000, err.Error()))
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeJSON(w, http.StatusOK, fail(40001, "unknown spot_id"))
		return
	}
	// 人工干预同样落事件流水，保证 events 是完整真相
	raw, _ := json.Marshal(in)
	_, _ = s.db.Exec(`INSERT INTO events
		(event_id, event_type, occur_time, camera_id, spot_id, spot_status, raw_json, received_at)
		VALUES(?,?,?,?,?,?,?,?)`,
		"srv-"+newUUID(), "OPERATOR_OVERRIDE", nowISO(), "", spotID, in.Status, string(raw), nowISO())
	writeJSON(w, http.StatusOK, ok(map[string]any{"spot_id": spotID, "status": in.Status}))
}

// ── 事件查询 ──

func (s *Server) handleQueryEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	where, args := []string{"1=1"}, []any{}
	for _, p := range []struct{ key, col string }{
		{"event_type", "event_type"}, {"camera_id", "camera_id"},
		{"track_id", "track_id"}, {"identity_id", "identity_id"},
	} {
		if v := q.Get(p.key); v != "" {
			where = append(where, p.col+"=?")
			args = append(args, v)
		}
	}
	if v := q.Get("from"); v != "" {
		where = append(where, "occur_time>=?")
		args = append(args, v)
	}
	if v := q.Get("to"); v != "" {
		where = append(where, "occur_time<=?")
		args = append(args, v)
	}
	size := 50
	if v := q.Get("size"); v != "" {
		fmt.Sscanf(v, "%d", &size)
	}
	if size > 200 {
		size = 200
	}
	page := 1
	if v := q.Get("page"); v != "" {
		fmt.Sscanf(v, "%d", &page)
	}
	args = append(args, size, (page-1)*size)

	rows, err := s.db.Query(`SELECT event_id, event_type, occur_time, camera_id, area_id,
		track_type, track_id, identity_status, identity_id, behavior,
		plate_no, spot_id, spot_status, snapshot_path
		FROM events WHERE `+strings.Join(where, " AND ")+`
		ORDER BY occur_time DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		writeJSON(w, http.StatusOK, fail(50000, err.Error()))
		return
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var e [14]string
		if err := rows.Scan(&e[0], &e[1], &e[2], &e[3], &e[4], &e[5], &e[6], &e[7], &e[8], &e[9], &e[10], &e[11], &e[12], &e[13]); err != nil {
			continue
		}
		list = append(list, map[string]any{
			"event_id": e[0], "event_type": e[1], "occur_time": e[2], "camera_id": e[3],
			"area_id": e[4], "track_type": e[5], "track_id": e[6],
			"identity_status": e[7], "identity_id": e[8], "behavior": e[9],
			"plate_no": e[10], "spot_id": e[11], "spot_status": e[12], "snapshot_url": e[13],
		})
	}
	writeJSON(w, http.StatusOK, ok(map[string]any{"list": list, "page": page, "size": size}))
}

// 报文提取辅助
func trackType(e *EventIn) string {
	if e.Track != nil {
		return e.Track.Type
	}
	return ""
}
func trackID(e *EventIn) string {
	if e.Track != nil {
		return e.Track.TrackID
	}
	return ""
}
func identityStatus(e *EventIn) string {
	if e.Person != nil && e.Person.Identity != nil {
		return e.Person.Identity.Status
	}
	return ""
}
func identityID(e *EventIn) string {
	if e.Person != nil && e.Person.Identity != nil {
		return e.Person.Identity.IdentityID
	}
	return ""
}
func identityConf(e *EventIn) float64 {
	if e.Person != nil && e.Person.Identity != nil {
		return e.Person.Identity.Confidence
	}
	return 0
}
func behavior(e *EventIn) string {
	if e.Person != nil {
		return e.Person.Behavior
	}
	return ""
}
func plateNo(e *EventIn) string {
	if e.Vehicle != nil {
		return e.Vehicle.PlateNo
	}
	return ""
}
func plateConf(e *EventIn) float64 {
	if e.Vehicle != nil {
		return e.Vehicle.Confidence
	}
	return 0
}
func spotID(e *EventIn) string {
	if e.Spot != nil {
		return e.Spot.SpotID
	}
	return ""
}
func spotStatus(e *EventIn) string {
	if e.Spot != nil {
		return e.Spot.Status
	}
	return ""
}
