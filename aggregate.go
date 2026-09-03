package main

import (
	"database/sql"
	"net/http"
	"time"
)

// 行为告警级别
var behaviorLevel = map[string]int{
	"NO_HELMET": 1, "NO_VEST": 1, "GATHERING": 1, "LOITERING": 1,
	"DANGER_ZONE": 2, "FALL_DOWN": 2,
}

const staleThresholdSec = 1800 // 30 分钟无 LEAVE 标记"待确认"
const camOfflineSec = 60       // 心跳超时判离线

type trackKey struct{ camera, track string }

func parseT(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	data := map[string]any{
		"server_time":      nowISO(),
		"cameras":          s.loadCameras(now),
		"persons_present":  s.loadPersonsPresent(now),
		"vehicles_present": s.loadVehiclesPresent(now),
		"spots":            s.loadSpots(now),
		"recent_alerts":    s.loadAlerts(),
		"stats":            s.loadStats(now),
	}
	writeJSON(w, http.StatusOK, ok(data))
}

func (s *Server) loadCameras(now time.Time) []map[string]any {
	rows, err := s.db.Query(`SELECT camera_id, status, fps, last_heartbeat, clock_offset_ms FROM cameras ORDER BY camera_id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, status, lastHb string
		var fps float64
		var offset int64
		if rows.Scan(&id, &status, &fps, &lastHb, &offset) != nil {
			continue
		}
		// 心跳超时一律判离线
		if lastHb == "" || now.Sub(parseT(lastHb)).Seconds() > camOfflineSec {
			status = "OFFLINE"
		}
		out = append(out, map[string]any{
			"camera_id": id, "status": status, "fps": fps,
			"last_heartbeat": lastHb, "clock_offset_ms": offset,
		})
	}
	return out
}

type presence struct {
	enterTS  string
	idStatus string // ENTER 事件自带的身份结论
	idID     string
}

// foldPresence 把 ENTER/LEAVE 事件折叠为"当前在场"集合，保留 ENTER 携带的身份信息
func (s *Server) foldPresence(enterType, leaveType string) map[trackKey]presence {
	rows, err := s.db.Query(`SELECT camera_id, track_id, event_type, occur_time, identity_status, identity_id
		FROM events WHERE event_type IN (?,?) ORDER BY occur_time ASC, id ASC`, enterType, leaveType)
	if err != nil {
		return nil
	}
	defer rows.Close()
	present := map[trackKey]presence{}
	for rows.Next() {
		var k trackKey
		var typ, ts, idStatus, idID string
		if rows.Scan(&k.camera, &k.track, &typ, &ts, &idStatus, &idID) != nil {
			continue
		}
		if typ == enterType {
			present[k] = presence{ts, idStatus, idID}
		} else {
			delete(present, k)
		}
	}
	return present
}

// latestPerTrack 取每个 track 最近一次某类事件的字段
func (s *Server) latestPerTrack(eventType string) map[trackKey]struct{ a, b string } {
	rows, err := s.db.Query(`SELECT camera_id, track_id, identity_status, identity_id FROM events
		WHERE event_type=? ORDER BY occur_time ASC, id ASC`, eventType)
	if err != nil {
		return nil
	}
	defer rows.Close()
	m := map[trackKey]struct{ a, b string }{}
	for rows.Next() {
		var k trackKey
		var v struct{ a, b string }
		if rows.Scan(&k.camera, &k.track, &v.a, &v.b) != nil {
			continue
		}
		m[k] = v
	}
	return m
}

func (s *Server) identityMap() map[string][2]string {
	rows, err := s.db.Query(`SELECT identity_id, name, role FROM identities`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	m := map[string][2]string{}
	for rows.Next() {
		var id, name, role string
		if rows.Scan(&id, &name, &role) != nil {
			continue
		}
		m[id] = [2]string{name, role}
	}
	return m
}

func (s *Server) loadPersonsPresent(now time.Time) []map[string]any {
	present := s.foldPresence("PERSON_ENTER", "PERSON_LEAVE")
	updates := s.latestPerTrack("IDENTITY_UPDATE")
	idents := s.identityMap()

	// 每个 track 最近的行为
	behLatest := map[trackKey]string{}
	rows, err := s.db.Query(`SELECT camera_id, track_id, behavior FROM events
		WHERE event_type='BEHAVIOR' ORDER BY occur_time ASC, id ASC`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var k trackKey
			var b string
			if rows.Scan(&k.camera, &k.track, &b) == nil {
				behLatest[k] = b
			}
		}
	}

	out := []map[string]any{}
	for k, pr := range present {
		dur := now.Sub(parseT(pr.enterTS)).Seconds()
		if dur < 0 {
			dur = 0
		}
		// 身份结论：优先最近一次 IDENTITY_UPDATE，否则用 ENTER 自带的
		idStatus, idID := pr.idStatus, pr.idID
		if u, ok2 := updates[k]; ok2 && u.a != "" {
			idStatus, idID = u.a, u.b
		}
		if idStatus == "" {
			idStatus = "UNRESOLVED"
		}
		name, role := "", ""
		if info, ok2 := idents[idID]; ok2 && idStatus == "IDENTIFIED" {
			name, role = info[0], info[1]
		}
		out = append(out, map[string]any{
			"track_id": k.track, "camera_id": k.camera,
			"enter_time": pr.enterTS, "duration_sec": int64(dur),
			"identity_status": idStatus, "identity_id": nilIfEmpty(idID),
			"name": nilIfEmpty(name), "role": nilIfEmpty(role),
			"last_behavior": nilIfEmpty(behLatest[k]),
			"stale":         dur > staleThresholdSec,
		})
	}
	return out
}

func (s *Server) loadVehiclesPresent(now time.Time) []map[string]any {
	present := s.foldPresence("VEHICLE_IN", "VEHICLE_OUT")
	// 每个 track 最近出现过的非空车牌
	plateLatest := map[trackKey]string{}
	rows, err := s.db.Query(`SELECT camera_id, track_id, plate_no FROM events
		WHERE event_type='VEHICLE_IN' AND plate_no<>'' ORDER BY occur_time ASC, id ASC`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var k trackKey
			var p string
			if rows.Scan(&k.camera, &k.track, &p) == nil {
				plateLatest[k] = p
			}
		}
	}
	out := []map[string]any{}
	for k, pr := range present {
		dur := now.Sub(parseT(pr.enterTS)).Seconds()
		if dur < 0 {
			dur = 0
		}
		out = append(out, map[string]any{
			"track_id": k.track, "camera_id": k.camera,
			"plate_no": plateLatest[k], "enter_time": pr.enterTS,
			"duration_sec": int64(dur),
		})
	}
	return out
}

func (s *Server) loadSpots(now time.Time) []map[string]any {
	rows, err := s.db.Query(`SELECT spot_id, zone, status, plate_no, since, overridden, target_minutes, updated_at
		FROM parking_spots ORDER BY spot_id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, zone, status, plate, since, updatedAt string
		var ovr, target int
		if rows.Scan(&id, &zone, &status, &plate, &since, &ovr, &target, &updatedAt) != nil {
			continue
		}
		var elapsed int64
		if since != "" {
			elapsed = int64(now.Sub(parseT(since)).Seconds())
			if elapsed < 0 {
				elapsed = 0
			}
		}
		out = append(out, map[string]any{
			"spot_id": id, "zone": zone, "status": status,
			"plate_no": nilIfEmpty(plate), "since": nilIfEmpty(since),
			"updated_at": nilIfEmpty(updatedAt), "elapsed_sec": elapsed,
			"over_target": status == "OCCUPIED" && target > 0 && elapsed > int64(target)*60,
			"overridden":  ovr == 1,
		})
	}
	return out
}

func (s *Server) loadAlerts() []map[string]any {
	idents := s.identityMap() // 先取映射：单连接下不能在 rows 未关闭时再查询
	rows, err := s.db.Query(`SELECT event_id, behavior, occur_time, camera_id, track_id,
		identity_status, identity_id, snapshot_path
		FROM events WHERE event_type='BEHAVIOR' ORDER BY occur_time DESC LIMIT 20`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var eid, beh, ts, cam, track, idStatus, idID, snap string
		if rows.Scan(&eid, &beh, &ts, &cam, &track, &idStatus, &idID, &snap) != nil {
			continue
		}
		target := track
		if idStatus == "STRANGER" {
			target = "STRANGER · " + track
		} else if info, ok2 := idents[idID]; ok2 {
			target = info[0] + " · " + idID
		}
		out = append(out, map[string]any{
			"event_id": eid, "behavior": beh, "level": behaviorLevel[beh],
			"occur_time": ts, "camera_id": cam, "target": target,
			"identity_status": nilIfEmpty(idStatus),
			"snapshot_url":    nilIfEmpty(snap),
			"notify_status":   "NONE", // 预留：WhatsApp/短信推送状态
		})
	}
	return out
}

func (s *Server) loadStats(now time.Time) map[string]any {
	// 本地零点为"今日"起点
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).
		Format("2006-01-02T15:04:05.000-07:00")

	var inCnt, outCnt, alertCnt int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE event_type='VEHICLE_IN' AND occur_time>=?`, dayStart).Scan(&inCnt)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE event_type='VEHICLE_OUT' AND occur_time>=?`, dayStart).Scan(&outCnt)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE event_type='BEHAVIOR' AND occur_time>=?`, dayStart).Scan(&alertCnt)

	// 平均维修时长：今日有完整 IN→OUT 的车辆，OUT-IN 求均值
	var avgSec sql.NullFloat64
	_ = s.db.QueryRow(`
		SELECT AVG(strftime('%s', o.occur_time) - strftime('%s', i.occur_time))
		FROM events o
		JOIN events i ON i.camera_id=o.camera_id AND i.track_id=o.track_id AND i.event_type='VEHICLE_IN'
		WHERE o.event_type='VEHICLE_OUT' AND o.occur_time>=? AND i.occur_time < o.occur_time`,
		dayStart).Scan(&avgSec)

	var occupied, total int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM parking_spots WHERE status='OCCUPIED'`).Scan(&occupied)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM parking_spots`).Scan(&total)
	rate := 0.0
	if total > 0 {
		rate = float64(occupied) / float64(total)
	}

	return map[string]any{
		"vehicles_in_today":  inCnt,
		"vehicles_out_today": outCnt,
		"alerts_today":       alertCnt,
		"avg_service_sec":    int64(avgSec.Float64),
		"spots_occupied":     occupied,
		"spots_total":        total,
		"occupancy_rate":     rate,
	}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
