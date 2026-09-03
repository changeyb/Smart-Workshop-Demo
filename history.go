package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

type historyEvent struct {
	eventID, eventType, occurTime, cameraID, areaID string
	trackID, identityStatus, identityID, behavior   string
	snapshotPath                                    string
	time                                            time.Time
}

type historySegment struct {
	events         []historyEvent
	firstEventID   string
	identityID     string
	identityStatus string
	hasEnter       bool
	hasLeave       bool
}

type historyQuery struct {
	from, to                     time.Time
	identityID, identityStatus   string
	personKey, keyword, cameraID string
	areaID                       string
	alertOnly                    bool
}

func parseHistoryQuery(r *http.Request) (historyQuery, string) {
	now := time.Now()
	q := historyQuery{
		from:           time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()),
		to:             now,
		identityID:     strings.TrimSpace(r.URL.Query().Get("identity_id")),
		identityStatus: strings.TrimSpace(r.URL.Query().Get("identity_status")),
		keyword:        strings.TrimSpace(r.URL.Query().Get("keyword")),
		cameraID:       strings.TrimSpace(r.URL.Query().Get("camera_id")),
		areaID:         strings.TrimSpace(r.URL.Query().Get("area_id")),
		alertOnly:      r.URL.Query().Get("alert_only") == "1" || r.URL.Query().Get("alert_only") == "true",
	}
	if q.identityStatus != "" && q.identityStatus != "IDENTIFIED" && q.identityStatus != "STRANGER" && q.identityStatus != "UNRESOLVED" {
		return q, "identity_status must be IDENTIFIED, STRANGER, or UNRESOLVED"
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
		track_id, identity_status, identity_id, behavior, snapshot_path
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
			&e.trackID, &e.identityStatus, &e.identityID, &e.behavior, &e.snapshotPath); err != nil {
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
			seg.identityStatus = segmentIdentityStatus(seg)
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
		if len(seg.events) == 0 {
			seg.firstEventID = e.eventID
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
		if q.identityID != "" && seg.identityID != q.identityID {
			continue
		}
		if q.personKey != "" && historyPersonKey(seg) != q.personKey {
			continue
		}
		if q.identityStatus != "" && seg.identityStatus != q.identityStatus {
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

func segmentIdentityStatus(seg *historySegment) string {
	if seg.identityID != "" {
		return "IDENTIFIED"
	}
	status := "UNRESOLVED"
	for _, e := range seg.events {
		if e.identityStatus == "STRANGER" || e.identityStatus == "UNRESOLVED" {
			status = e.identityStatus
		}
	}
	return status
}

func encodedHistoryKey(prefix, value string) string {
	return prefix + base64.RawURLEncoding.EncodeToString([]byte(value))
}

func historyPersonKey(seg historySegment) string {
	if seg.identityID != "" {
		return encodedHistoryKey("id_", seg.identityID)
	}
	return encodedHistoryKey("obs_", seg.firstEventID)
}

type historySummary struct {
	PersonKey          string   `json:"person_key"`
	IdentityStatus     string   `json:"identity_status"`
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
	TrackIDs           []string `json:"track_ids"`
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
		first, last              time.Time
		cameras, areas, trackIDs map[string]bool
	}
	byID := map[string]*accumulator{}
	for _, seg := range segments {
		personKey := historyPersonKey(seg)
		a := byID[personKey]
		if a == nil {
			info := identities[seg.identityID]
			a = &accumulator{
				historySummary: historySummary{PersonKey: personKey, IdentityStatus: seg.identityStatus, IdentityID: seg.identityID, Name: info.name, EmployeeNo: info.employeeNo, Role: info.role},
				cameras:        map[string]bool{}, areas: map[string]bool{}, trackIDs: map[string]bool{},
			}
			byID[personKey] = a
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
			if e.trackID != "" {
				a.trackIDs[e.trackID] = true
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
		for v := range a.trackIDs {
			a.TrackIDs = append(a.TrackIDs, v)
		}
		sort.Strings(a.Cameras)
		sort.Strings(a.Areas)
		sort.Strings(a.TrackIDs)
		out = append(out, a.historySummary)
	}
	sort.Slice(out, func(i, j int) bool { return parseT(out[i].LastSeen).After(parseT(out[j].LastSeen)) })
	return out, nil
}

func filterHistoryKeyword(items []historySummary, keyword string) []historySummary {
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	if keyword == "" {
		return items
	}
	kept := items[:0]
	for _, item := range items {
		values := []string{item.Name, item.EmployeeNo, item.IdentityID}
		values = append(values, item.TrackIDs...)
		matched := false
		for _, value := range values {
			if strings.Contains(strings.ToLower(value), keyword) {
				matched = true
				break
			}
		}
		if matched {
			kept = append(kept, item)
		}
	}
	return kept
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
	summaries = filterHistoryKeyword(summaries, q.keyword)
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
	key := r.PathValue("key")
	if key == "" {
		writeJSON(w, http.StatusOK, fail(40001, "missing person key"))
		return
	}
	if strings.HasPrefix(key, "id_") {
		encoded := strings.TrimPrefix(key, "id_")
		decoded, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || len(decoded) == 0 {
			writeJSON(w, http.StatusOK, fail(40001, "invalid person key"))
			return
		}
		q.identityID = string(decoded)
	} else if strings.HasPrefix(key, "obs_") {
		encoded := strings.TrimPrefix(key, "obs_")
		decoded, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || len(decoded) == 0 {
			writeJSON(w, http.StatusOK, fail(40001, "invalid person key"))
			return
		}
		q.personKey = key
	} else {
		q.identityID = key
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
				"identity_status": nilIfEmpty(e.identityStatus), "identity_id": nilIfEmpty(e.identityID),
				"behavior": nilIfEmpty(e.behavior), "snapshot_url": nilIfEmpty(e.snapshotPath),
			})
		}
	}
	sort.Slice(nodes, func(i, j int) bool {
		return fmt.Sprint(nodes[i]["occur_time"]) < fmt.Sprint(nodes[j]["occur_time"])
	})
	writeJSON(w, http.StatusOK, ok(map[string]any{"summary": summaries[0], "nodes": nodes}))
}
