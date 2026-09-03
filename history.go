package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

type historyEvent struct {
	eventID, eventType, occurTime, cameraID, areaID string
	trackID, identityID, behavior                   string
	snapshotPath                                    string
	time                                            time.Time
}

type historySegment struct {
	events     []historyEvent
	identityID string
	hasEnter   bool
	hasLeave   bool
}

type historyQuery struct {
	from, to             time.Time
	identityID, cameraID string
	areaID               string
	alertOnly            bool
}

func parseHistoryQuery(r *http.Request) (historyQuery, string) {
	now := time.Now()
	q := historyQuery{
		from:       time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()),
		to:         now,
		identityID: strings.TrimSpace(r.URL.Query().Get("identity_id")),
		cameraID:   strings.TrimSpace(r.URL.Query().Get("camera_id")),
		areaID:     strings.TrimSpace(r.URL.Query().Get("area_id")),
		alertOnly:  r.URL.Query().Get("alert_only") == "1" || r.URL.Query().Get("alert_only") == "true",
	}
	for _, item := range []struct {
		name string
		dst  *time.Time
	}{{"from", &q.from}, {"to", &q.to}} {
		if raw := r.URL.Query().Get(item.name); raw != "" {
			parsed, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				return q, item.name + " must be RFC3339"
			}
			*item.dst = parsed
		}
	}
	if q.to.Before(q.from) {
		return q, "to must not be before from"
	}
	if q.to.Sub(q.from) > 31*24*time.Hour {
		return q, "time range must not exceed 31 days"
	}
	return q, ""
}

func (s *Server) loadHistorySegments(q historyQuery) ([]historySegment, error) {
	rows, err := s.db.Query(`SELECT event_id, event_type, occur_time, camera_id, area_id,
		track_id, identity_id, behavior, snapshot_path
		FROM events
		WHERE event_type IN ('PERSON_ENTER','PERSON_LEAVE','IDENTITY_UPDATE','BEHAVIOR')
		ORDER BY occur_time ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var all []historyEvent
	for rows.Next() {
		var e historyEvent
		if err := rows.Scan(&e.eventID, &e.eventType, &e.occurTime, &e.cameraID, &e.areaID,
			&e.trackID, &e.identityID, &e.behavior, &e.snapshotPath); err != nil {
			return nil, err
		}
		e.time = parseT(e.occurTime)
		if e.time.IsZero() || e.trackID == "" || e.time.After(q.to) {
			continue
		}
		all = append(all, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	active := map[trackKey]*historySegment{}
	segments := []historySegment{}
	finish := func(seg *historySegment) {
		if seg != nil && len(seg.events) > 0 {
			segments = append(segments, *seg)
		}
	}
	for _, e := range all {
		key := trackKey{camera: e.cameraID, track: e.trackID}
		seg := active[key]
		switch e.eventType {
		case "PERSON_ENTER":
			finish(seg)
			seg = &historySegment{hasEnter: true}
			active[key] = seg
		case "PERSON_LEAVE":
			if seg == nil {
				seg = &historySegment{}
			}
			seg.hasLeave = true
		default:
			if seg == nil {
				seg = &historySegment{}
				active[key] = seg
			}
		}
		seg.events = append(seg.events, e)
		if e.identityID != "" {
			seg.identityID = e.identityID
		}
		if e.eventType == "PERSON_LEAVE" {
			finish(seg)
			delete(active, key)
		}
	}
	for _, seg := range active {
		finish(seg)
	}

	filtered := segments[:0]
	for _, seg := range segments {
		if seg.identityID == "" || (q.identityID != "" && seg.identityID != q.identityID) {
			continue
		}
		kept := seg.events[:0]
		for _, e := range seg.events {
			if e.time.Before(q.from) || e.time.After(q.to) {
				continue
			}
			if q.cameraID != "" && e.cameraID != q.cameraID {
				continue
			}
			if q.areaID != "" && e.areaID != q.areaID {
				continue
			}
			kept = append(kept, e)
		}
		if len(kept) == 0 {
			continue
		}
		seg.events = kept
		filtered = append(filtered, seg)
	}
	return filtered, nil
}

type historySummary struct {
	IdentityID         string   `json:"identity_id"`
	Name               string   `json:"name"`
	EmployeeNo         string   `json:"employee_no"`
	Role               string   `json:"role"`
	FirstSeen          string   `json:"first_seen"`
	LastSeen           string   `json:"last_seen"`
	DurationSec        int64    `json:"duration_sec"`
	EventCount         int      `json:"event_count"`
	SegmentCount       int      `json:"segment_count"`
	AlertCount         int      `json:"alert_count"`
	IncompleteSegments int      `json:"incomplete_segments"`
	Cameras            []string `json:"cameras"`
	Areas              []string `json:"areas"`
}

func (s *Server) summarizeHistory(segments []historySegment) ([]historySummary, error) {
	type identityRow struct{ name, employeeNo, role string }
	identities := map[string]identityRow{}
	rows, err := s.db.Query(`SELECT identity_id, name, employee_no, role FROM identities`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var item identityRow
		if err := rows.Scan(&id, &item.name, &item.employeeNo, &item.role); err != nil {
			rows.Close()
			return nil, err
		}
		identities[id] = item
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	type accumulator struct {
		historySummary
		first, last    time.Time
		cameras, areas map[string]bool
	}
	byID := map[string]*accumulator{}
	for _, seg := range segments {
		a := byID[seg.identityID]
		if a == nil {
			info := identities[seg.identityID]
			a = &accumulator{
				historySummary: historySummary{IdentityID: seg.identityID, Name: info.name, EmployeeNo: info.employeeNo, Role: info.role},
				cameras:        map[string]bool{}, areas: map[string]bool{},
			}
			byID[seg.identityID] = a
		}
		a.SegmentCount++
		if !seg.hasEnter || !seg.hasLeave {
			a.IncompleteSegments++
		}
		segmentFirst, segmentLast := seg.events[0].time, seg.events[len(seg.events)-1].time
		if a.first.IsZero() || segmentFirst.Before(a.first) {
			a.first = segmentFirst
		}
		if a.last.IsZero() || segmentLast.After(a.last) {
			a.last = segmentLast
		}
		if segmentLast.After(segmentFirst) {
			a.DurationSec += int64(segmentLast.Sub(segmentFirst).Seconds())
		}
		for _, e := range seg.events {
			a.EventCount++
			if e.eventType == "BEHAVIOR" {
				a.AlertCount++
			}
			if e.cameraID != "" {
				a.cameras[e.cameraID] = true
			}
			if e.areaID != "" {
				a.areas[e.areaID] = true
			}
		}
	}

	out := make([]historySummary, 0, len(byID))
	for _, a := range byID {
		a.FirstSeen, a.LastSeen = a.first.Format(time.RFC3339Nano), a.last.Format(time.RFC3339Nano)
		for v := range a.cameras {
			a.Cameras = append(a.Cameras, v)
		}
		for v := range a.areas {
			a.Areas = append(a.Areas, v)
		}
		sort.Strings(a.Cameras)
		sort.Strings(a.Areas)
		out = append(out, a.historySummary)
	}
	sort.Slice(out, func(i, j int) bool { return parseT(out[i].LastSeen).After(parseT(out[j].LastSeen)) })
	return out, nil
}

func (s *Server) historyFilterMeta() map[string]any {
	employees := []map[string]string{}
	rows, err := s.db.Query(`SELECT identity_id, name, employee_no, role FROM identities ORDER BY name, identity_id`)
	if err == nil {
		for rows.Next() {
			var id, name, no, role string
			if rows.Scan(&id, &name, &no, &role) == nil {
				employees = append(employees, map[string]string{"identity_id": id, "name": name, "employee_no": no, "role": role})
			}
		}
		rows.Close()
	}
	cameras, areas := []string{}, []string{}
	for _, spec := range []struct {
		query string
		dst   *[]string
	}{
		{`SELECT camera_id FROM cameras ORDER BY camera_id`, &cameras},
		{`SELECT DISTINCT area_id FROM events WHERE area_id<>'' ORDER BY area_id`, &areas},
	} {
		rows, err := s.db.Query(spec.query)
		if err != nil {
			continue
		}
		for rows.Next() {
			var value string
			if rows.Scan(&value) == nil {
				*spec.dst = append(*spec.dst, value)
			}
		}
		rows.Close()
	}
	return map[string]any{"employees": employees, "cameras": cameras, "areas": areas}
}

func (s *Server) handlePersonHistory(w http.ResponseWriter, r *http.Request) {
	q, reason := parseHistoryQuery(r)
	if reason != "" {
		writeJSON(w, http.StatusOK, fail(40001, reason))
		return
	}
	segments, err := s.loadHistorySegments(q)
	if err != nil {
		writeJSON(w, http.StatusOK, fail(50000, err.Error()))
		return
	}
	summaries, err := s.summarizeHistory(segments)
	if err != nil {
		writeJSON(w, http.StatusOK, fail(50000, err.Error()))
		return
	}
	if q.alertOnly {
		kept := summaries[:0]
		for _, item := range summaries {
			if item.AlertCount > 0 {
				kept = append(kept, item)
			}
		}
		summaries = kept
	}
	writeJSON(w, http.StatusOK, ok(map[string]any{
		"list": summaries, "from": q.from.Format(time.RFC3339Nano), "to": q.to.Format(time.RFC3339Nano),
		"server_time": nowISO(), "filters": s.historyFilterMeta(),
	}))
}

func (s *Server) handlePersonHistoryDetail(w http.ResponseWriter, r *http.Request) {
	q, reason := parseHistoryQuery(r)
	if reason != "" {
		writeJSON(w, http.StatusOK, fail(40001, reason))
		return
	}
	q.identityID = r.PathValue("identity_id")
	if q.identityID == "" {
		writeJSON(w, http.StatusOK, fail(40001, "missing identity_id"))
		return
	}
	segments, err := s.loadHistorySegments(q)
	if err != nil {
		writeJSON(w, http.StatusOK, fail(50000, err.Error()))
		return
	}
	summaries, err := s.summarizeHistory(segments)
	if err != nil {
		writeJSON(w, http.StatusOK, fail(50000, err.Error()))
		return
	}
	if len(summaries) == 0 {
		writeJSON(w, http.StatusOK, fail(40401, "person history not found"))
		return
	}
	nodes := []map[string]any{}
	for _, seg := range segments {
		for _, e := range seg.events {
			nodes = append(nodes, map[string]any{
				"event_id": e.eventID, "event_type": e.eventType, "occur_time": e.occurTime,
				"camera_id": e.cameraID, "area_id": e.areaID, "track_id": e.trackID,
				"behavior": nilIfEmpty(e.behavior), "snapshot_url": nilIfEmpty(e.snapshotPath),
			})
		}
	}
	sort.Slice(nodes, func(i, j int) bool {
		return fmt.Sprint(nodes[i]["occur_time"]) < fmt.Sprint(nodes[j]["occur_time"])
	})
	writeJSON(w, http.StatusOK, ok(map[string]any{"summary": summaries[0], "nodes": nodes}))
}
