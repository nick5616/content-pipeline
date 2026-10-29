package jobs

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// State is a job's state (openapi JobState).
type State string

const (
	Pending State = "pending"
	Running State = "running"
	Done    State = "done"
	Failed  State = "failed"
)

// Job is a job as the API shows it (openapi Job).
type Job struct {
	Jobkey    string  `json:"jobkey"`
	Recipe    string  `json:"recipe"`
	Source    string  `json:"source"` // sha256:<hex>
	Item      *string `json:"item"`
	State     State   `json:"state"`
	Attempts  int     `json:"attempts"`
	LastError *string `json:"lastError"`
}

// DB is the local job state, <state>/jobs.db. It is a record of what ran
// here, not the source of truth: the derived cache and the bucket's
// receipts decide whether a job is done.
type DB struct{ db *sql.DB }

// ErrNotFound means no job has that key.
var ErrNotFound = errors.New("no such job")

// OpenDB opens (creating if needed) the job database in stateDir.
func OpenDB(stateDir string) (*DB, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(stateDir, "jobs.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS jobs (
		jobkey     TEXT PRIMARY KEY,
		recipe     TEXT NOT NULL,
		source     TEXT NOT NULL,
		item       TEXT,
		state      TEXT NOT NULL,
		attempts   INTEGER NOT NULL DEFAULT 0,
		last_error TEXT,
		updated    TEXT NOT NULL
	)`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("jobs.db: %w", err)
	}
	return &DB{db}, nil
}

func (d *DB) Close() error { return d.db.Close() }

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// ensure records a spec as pending unless it's already known.
func (d *DB) ensure(s Spec) error {
	_, err := d.db.Exec(`INSERT INTO jobs (jobkey, recipe, source, item, state, updated) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(jobkey) DO UPDATE SET item = excluded.item`,
		s.Key, s.Recipe.ID(), "sha256:"+s.Source, string(s.Item), Pending, now())
	return err
}

// set moves a job to state. attempt counts a new run.
func (d *DB) set(key string, st State, lastErr *string, attempt bool) error {
	inc := 0
	if attempt {
		inc = 1
	}
	q := `UPDATE jobs SET state = ?, attempts = attempts + ?, updated = ?`
	args := []any{st, inc, now()}
	if st != Running {
		q += `, last_error = ?`
		args = append(args, lastErr)
	}
	_, err := d.db.Exec(q+` WHERE jobkey = ?`, append(args, key)...)
	return err
}

// Get returns one job.
func (d *DB) Get(key string) (Job, error) {
	rows, err := d.db.Query(`SELECT jobkey, recipe, source, item, state, attempts, last_error FROM jobs WHERE jobkey = ?`, key)
	if err != nil {
		return Job{}, err
	}
	js, err := scan(rows)
	if err != nil {
		return Job{}, err
	}
	if len(js) == 0 {
		return Job{}, ErrNotFound
	}
	return js[0], nil
}

// List returns jobs, newest first, optionally only those in state.
func (d *DB) List(state State) ([]Job, error) {
	q := `SELECT jobkey, recipe, source, item, state, attempts, last_error FROM jobs`
	var args []any
	if state != "" {
		q += ` WHERE state = ?`
		args = append(args, state)
	}
	rows, err := d.db.Query(q+` ORDER BY updated DESC, jobkey`, args...)
	if err != nil {
		return nil, err
	}
	return scan(rows)
}

// Retry queues a failed job again.
func (d *DB) Retry(key string) (Job, error) {
	j, err := d.Get(key)
	if err != nil {
		return Job{}, err
	}
	if j.State != Failed {
		return Job{}, fmt.Errorf("job %s is %s, not failed", key, j.State)
	}
	if err := d.set(key, Pending, j.LastError, false); err != nil {
		return Job{}, err
	}
	return d.Get(key)
}

func scan(rows *sql.Rows) ([]Job, error) {
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		var j Job
		var item, lastErr sql.NullString
		if err := rows.Scan(&j.Jobkey, &j.Recipe, &j.Source, &item, &j.State, &j.Attempts, &lastErr); err != nil {
			return nil, err
		}
		if item.Valid && item.String != "" {
			j.Item = &item.String
		}
		if lastErr.Valid {
			j.LastError = &lastErr.String
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
