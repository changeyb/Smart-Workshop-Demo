package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

func openDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite 单写者：限制连接数避免 database is locked
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("init schema: %w", err)
	}
	if err := seed(db); err != nil {
		return nil, fmt.Errorf("seed: %w", err)
	}
	return db, nil
}

const schema = `
CREATE TABLE IF NOT EXISTS events (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id      TEXT NOT NULL UNIQUE,
  event_type    TEXT NOT NULL,
  occur_time    TEXT NOT NULL,
  camera_id     TEXT NOT NULL DEFAULT '',
  area_id       TEXT NOT NULL DEFAULT '',
  track_type    TEXT NOT NULL DEFAULT '',
  track_id      TEXT NOT NULL DEFAULT '',
  identity_status TEXT NOT NULL DEFAULT '',
  identity_id   TEXT NOT NULL DEFAULT '',
  identity_conf REAL NOT NULL DEFAULT 0,
  behavior      TEXT NOT NULL DEFAULT '',
  plate_no      TEXT NOT NULL DEFAULT '',
  plate_conf    REAL NOT NULL DEFAULT 0,
  spot_id       TEXT NOT NULL DEFAULT '',
  spot_status   TEXT NOT NULL DEFAULT '',
  snapshot_path TEXT NOT NULL DEFAULT '',
  raw_json      TEXT NOT NULL DEFAULT '{}',
  received_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_track ON events(camera_id, track_id, occur_time);
CREATE INDEX IF NOT EXISTS idx_events_type_time ON events(event_type, occur_time);

CREATE TABLE IF NOT EXISTS parking_spots (
  spot_id        TEXT PRIMARY KEY,
  zone           TEXT NOT NULL DEFAULT 'A',
  status         TEXT NOT NULL DEFAULT 'FREE',
  plate_no       TEXT NOT NULL DEFAULT '',
  since          TEXT NOT NULL DEFAULT '',
  overridden     INTEGER NOT NULL DEFAULT 0,
  target_minutes INTEGER NOT NULL DEFAULT 120,
  updated_at     TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS cameras (
  camera_id       TEXT PRIMARY KEY,
  status          TEXT NOT NULL DEFAULT 'OFFLINE',
  fps             REAL NOT NULL DEFAULT 0,
  device_id       TEXT NOT NULL DEFAULT '',
  last_heartbeat  TEXT NOT NULL DEFAULT '',
  clock_offset_ms INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS identities (
  identity_id  TEXT PRIMARY KEY,
  name         TEXT NOT NULL DEFAULT '',
  employee_no  TEXT NOT NULL DEFAULT '',
  role         TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL DEFAULT ''
);
`

func seed(db *sql.DB) error {
	// 工位：A 区 6 个，超时阈值 120 分钟。
	for i := 1; i <= 6; i++ {
		_, err := db.Exec(
			`INSERT OR IGNORE INTO parking_spots(spot_id, zone, status, target_minutes) VALUES(?,?, 'FREE', 120)`,
			fmt.Sprintf("A-%02d", i), "A")
		if err != nil {
			return err
		}
	}
	// 旧版默认创建了 A-07 至 A-12，升级时仅移除这些默认工位状态，保留事件流水。
	if _, err := db.Exec(`DELETE FROM parking_spots WHERE spot_id IN ('A-07','A-08','A-09','A-10','A-11','A-12')`); err != nil {
		return err
	}
	// 摄像头登记
	for _, c := range []string{"CAM_001", "CAM_002", "CAM_003", "CAM_004"} {
		if _, err := db.Exec(`INSERT OR IGNORE INTO cameras(camera_id) VALUES(?)`, c); err != nil {
			return err
		}
	}
	// 身份映射（人脸库在算法侧，这里只存 identity_id → 姓名/角色）
	type iden struct{ id, name, no, role string }
	for _, p := range []iden{
		{"EMP_10086", "Tan Wei Ming", "EMP_10086", "Senior Mechanic"},
		{"EMP_10023", "Ahmad Yusof", "EMP_10023", "Mechanic"},
		{"EMP_10047", "Li Na", "EMP_10047", "Service Advisor"},
		{"EMP_10031", "Raj Kumar", "EMP_10031", "Panel Beater"},
	} {
		_, err := db.Exec(
			`INSERT OR IGNORE INTO identities(identity_id, name, employee_no, role, created_at) VALUES(?,?,?,?,datetime('now'))`,
			p.id, p.name, p.no, p.role)
		if err != nil {
			return err
		}
	}
	return nil
}
