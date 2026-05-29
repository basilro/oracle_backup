package main

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

type Run struct {
	ID         int64
	Trigger    string
	Status     string
	StartedAt  string
	FinishedAt string
	ExitCode   int
	DataAdded  int64
	SnapshotID string
	DBSummary  string
	LogPath    string
	Error      string
}

type Store struct{ db *sql.DB }

func OpenStore(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	schema := `CREATE TABLE IF NOT EXISTS runs(
		id INTEGER PRIMARY KEY AUTOINCREMENT, trigger TEXT, status TEXT,
		started_at TEXT, finished_at TEXT, exit_code INTEGER DEFAULT 0,
		data_added INTEGER DEFAULT 0, snapshot_id TEXT, db_summary TEXT, log_path TEXT, error TEXT);
	CREATE TABLE IF NOT EXISTS audit(
		id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT, actor TEXT, action TEXT, result TEXT);`
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	return &Store{db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

func (s *Store) StartRun(trigger string) (int64, error) {
	r, err := s.db.Exec(`INSERT INTO runs(trigger,status,started_at) VALUES(?, 'running', ?)`, trigger, nowUTC())
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func (s *Store) FinishRun(id int64, status string, exit int, data int64, snap, dbsum, errmsg string) error {
	_, err := s.db.Exec(`UPDATE runs SET status=?,finished_at=?,exit_code=?,data_added=?,snapshot_id=?,db_summary=?,error=? WHERE id=?`,
		status, nowUTC(), exit, data, snap, dbsum, errmsg, id)
	return err
}

func (s *Store) SetLog(id int64, path string) error {
	_, err := s.db.Exec(`UPDATE runs SET log_path=? WHERE id=?`, path, id)
	return err
}

func (s *Store) ReconcileStuck() error {
	_, err := s.db.Exec(`UPDATE runs SET status='interrupted',finished_at=? WHERE status='running'`, nowUTC())
	return err
}

const runCols = `id,trigger,status,started_at,IFNULL(finished_at,''),exit_code,data_added,IFNULL(snapshot_id,''),IFNULL(db_summary,''),IFNULL(log_path,''),IFNULL(error,'')`

func scanRun(sc interface{ Scan(...any) error }) (Run, error) {
	var r Run
	err := sc.Scan(&r.ID, &r.Trigger, &r.Status, &r.StartedAt, &r.FinishedAt, &r.ExitCode, &r.DataAdded, &r.SnapshotID, &r.DBSummary, &r.LogPath, &r.Error)
	return r, err
}

func (s *Store) GetRun(id int64) (Run, error) {
	return scanRun(s.db.QueryRow(`SELECT `+runCols+` FROM runs WHERE id=?`, id))
}

func (s *Store) ListRuns(limit, offset int) ([]Run, error) {
	rows, err := s.db.Query(`SELECT `+runCols+` FROM runs ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Audit(actor, action, result string) {
	_, _ = s.db.Exec(`INSERT INTO audit(ts,actor,action,result) VALUES(?,?,?,?)`, nowUTC(), actor, action, result)
}
