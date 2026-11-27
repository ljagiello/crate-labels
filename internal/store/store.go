// Package store keeps crate-labels' state in one SQLite file: settings, saved printers,
// the Discogs response and image caches, the tempo cache, crates, print jobs, print
// history, and the label-print log that says which records already have a label.
//
// The Discogs personal access token is never stored here. discogs_identity only records
// which account the token belongs to and where the token is kept (the macOS Keychain).
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// FileName is the database file inside DataDir. Pre-release builds wrote a different
// file ("crate-labels.db") with an older schema; that file is never opened, so it is
// left alone rather than migrated. It can be deleted.
const FileName = "crate-labels.sqlite"

// SchemaVersion is the newest schema this build knows (PRAGMA user_version).
const SchemaVersion = 1

// applicationID marks the file as ours (PRAGMA application_id, the ASCII bytes "crlb").
// A database without it, or with another program's id, is refused instead of altered.
const applicationID = 0x63726c62

// Defaults for the limits a Store enforces.
const (
	DefaultHistoryLimit = 500 // enough for a long crate run
	// DefaultCrateNameMax keeps a crate name on one line of the crate list.
	DefaultCrateNameMax = 40
	// DefaultCrateRecordsMax matches the most records one print job takes
	// (service.MaxItems), so "print the whole crate" always fits in a single job.
	DefaultCrateRecordsMax = 500
	DefaultCacheMaxAge     = 31 * 24 * time.Hour // the longest response TTL is 30 days
	DefaultCacheMaxBytes   = 64 << 20
	DefaultImageMaxAge     = 90 * 24 * time.Hour
	DefaultImageMaxBytes   = 256 << 20
	DefaultTempoMaxAge     = 365 * 24 * time.Hour // a tempo does not go stale; files move
	DefaultTempoMaxBytes   = 8 << 20              // ~25,000 results
	pruneEvery             = 64                   // cache writes between prunes
)

// schemaV1 is the whole first schema. Later versions append a step to migrations; a
// released step is never edited.
const schemaV1 = `
CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL                -- JSON
);

CREATE TABLE discogs_identity (
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  username   TEXT NOT NULL,
  token_ref  TEXT NOT NULL,          -- where the token is kept, never the token
  checked_at INTEGER NOT NULL
);

CREATE TABLE response_cache (
  url        TEXT PRIMARY KEY,       -- the full request URL without the token
  body       TEXT NOT NULL,
  fetched_at INTEGER NOT NULL
);
CREATE TABLE image_cache (
  url          TEXT PRIMARY KEY,
  content_type TEXT NOT NULL,
  body         BLOB NOT NULL,
  fetched_at   INTEGER NOT NULL
);
CREATE TABLE tempo_cache (
  key         TEXT PRIMARY KEY,      -- a hash; see service.DetectTempo
  result_json TEXT NOT NULL,
  created_at  INTEGER NOT NULL
);

CREATE TABLE printers (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  model_id      TEXT NOT NULL,
  address       TEXT NOT NULL DEFAULT '',
  settings_json TEXT NOT NULL DEFAULT '{}',
  created_at    INTEGER NOT NULL,
  last_used_at  INTEGER
);

CREATE TABLE crates (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE COLLATE NOCASE CHECK (length(name) > 0),
  created_at INTEGER NOT NULL
);
-- A crate lists each release once. The artist, title, catno, year and thumb columns are
-- what the crate list shows without asking Discogs again.
CREATE TABLE crate_records (
  id         INTEGER PRIMARY KEY,
  crate_id   TEXT NOT NULL REFERENCES crates(id) ON DELETE CASCADE,
  position   INTEGER NOT NULL,
  release_id INTEGER NOT NULL,
  artist     TEXT NOT NULL DEFAULT '',
  title      TEXT NOT NULL DEFAULT '',
  catno      TEXT NOT NULL DEFAULT '',
  year       INTEGER,
  thumb      TEXT NOT NULL DEFAULT '',
  data_json  TEXT,                   -- an edited label; NULL = build it from Discogs
  edited_at  INTEGER,                -- when data_json was last saved
  added_at   INTEGER NOT NULL,
  UNIQUE (crate_id, release_id)
);
CREATE INDEX crate_records_by_position ON crate_records (crate_id, position);

CREATE TABLE print_jobs (
  id                 INTEGER PRIMARY KEY,
  created_at         INTEGER NOT NULL,
  started_at         INTEGER,
  finished_at        INTEGER,
  status             TEXT NOT NULL,
  owner              TEXT NOT NULL DEFAULT '',  -- "<pid>:<start time>" of the queuing process
  printer_id         TEXT NOT NULL,
  printer_name       TEXT NOT NULL DEFAULT '',
  template           TEXT NOT NULL,
  stock              TEXT NOT NULL,
  options_json       TEXT NOT NULL DEFAULT '{}',
  print_options_json TEXT NOT NULL DEFAULT '{}',
  records_json       TEXT NOT NULL,            -- the label data of each record, frozen at queue time
  copies             INTEGER NOT NULL DEFAULT 1,
  total              INTEGER NOT NULL DEFAULT 0,
  printed            INTEGER NOT NULL DEFAULT 0,
  progress           REAL NOT NULL DEFAULT 0,
  message            TEXT NOT NULL DEFAULT '',
  error              TEXT,
  source             TEXT NOT NULL DEFAULT ''
);
CREATE INDEX print_jobs_by_status ON print_jobs (status);

-- What was printed, newest first, for reprinting. Capped and de-duplicated, so it is
-- not the record of what has a label; label_prints is.
CREATE TABLE print_history (
  id           INTEGER PRIMARY KEY,
  printed_at   INTEGER NOT NULL,
  template     TEXT NOT NULL,
  stock        TEXT NOT NULL,
  printer      TEXT NOT NULL DEFAULT '',
  artist       TEXT NOT NULL DEFAULT '',
  title        TEXT NOT NULL DEFAULT '',
  catno        TEXT NOT NULL DEFAULT '',
  year         TEXT NOT NULL DEFAULT '',
  label        TEXT NOT NULL DEFAULT '',
  format       TEXT NOT NULL DEFAULT '',
  release_id   INTEGER,
  copies       INTEGER NOT NULL DEFAULT 1,
  options_json TEXT NOT NULL DEFAULT '{}',
  data_json    TEXT NOT NULL,
  same_as      TEXT NOT NULL             -- equal for equal (data, template, stock, options)
);
CREATE INDEX print_history_newest ON print_history (printed_at DESC);

-- One row for every record a job printed, kept for good. A release "has a label" when
-- it has a row here, unless printed_overrides says otherwise. The labels-printed total
-- is SUM(copies).
CREATE TABLE label_prints (
  id         INTEGER PRIMARY KEY,
  release_id INTEGER,                    -- NULL: typed by hand, or a test print
  job_id     INTEGER REFERENCES print_jobs(id) ON DELETE SET NULL,
  template   TEXT NOT NULL,
  copies     INTEGER NOT NULL CHECK (copies > 0),
  printed_at INTEGER NOT NULL
);
CREATE INDEX label_prints_by_release ON label_prints (release_id) WHERE release_id IS NOT NULL;

-- The user's word on whether a release has a label, over what label_prints says. No row
-- means "auto".
CREATE TABLE printed_overrides (
  release_id INTEGER PRIMARY KEY,
  state      TEXT NOT NULL CHECK (state IN ('printed', 'not_printed')),
  set_at     INTEGER NOT NULL
);
`

// migrations[v-1] brings a database from version v-1 to v.
var migrations = []string{schemaV1}

// DataDir is where crate-labels keeps its data: $CRATE_LABELS_DATA, or
// ~/Library/Application Support/crate-labels.
func DataDir() string {
	if d := os.Getenv("CRATE_LABELS_DATA"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "crate-labels")
}

// DefaultPath is the database file inside DataDir.
func DefaultPath() string { return filepath.Join(DataDir(), FileName) }

// NotFoundError says a row the caller asked for does not exist (HTTP 404).
type NotFoundError struct{ Msg string }

func (e *NotFoundError) Error() string { return e.Msg }

// InvalidError says a write breaks a rule, e.g. a name is too long or a crate is full
// (HTTP 400).
type InvalidError struct{ Msg string }

func (e *InvalidError) Error() string { return e.Msg }

func notFound(format string, args ...any) error {
	return &NotFoundError{Msg: fmt.Sprintf(format, args...)}
}

func invalid(format string, args ...any) error {
	return &InvalidError{Msg: fmt.Sprintf(format, args...)}
}

// IsNotFound reports whether err is (or wraps) a *NotFoundError.
func IsNotFound(err error) bool {
	var nf *NotFoundError
	return errors.As(err, &nf)
}

// Store is one SQLite file. It is safe for concurrent use.
type Store struct {
	db   *sql.DB
	path string

	// Now is the clock; tests replace it. It returns wall time.
	Now func() time.Time
	// HistoryLimit is how many history entries are kept.
	HistoryLimit int
	// CrateNameMax is the longest crate name allowed, in characters.
	CrateNameMax int
	// CrateRecordsMax is how many records a crate holds.
	CrateRecordsMax int
	// Cache limits: Prune drops entries older than the max age, then the oldest ones
	// until the bodies fit in the max bytes.
	CacheMaxAge, ImageMaxAge, TempoMaxAge       time.Duration
	CacheMaxBytes, ImageMaxBytes, TempoMaxBytes int64

	puts atomic.Int64 // cache writes since the last prune
}

// Open opens (creating if needed) the database at path. Pass "" for DefaultPath and
// ":memory:" for a private in-memory database.
func Open(path string) (*Store, error) {
	if path == "" {
		path = DefaultPath()
	}
	var dsn string
	if path == ":memory:" {
		dsn = "file::memory:?_pragma=foreign_keys(1)&_txlock=immediate"
	} else {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create the data folder: %w", err)
		}
		// Only connection settings here: nothing in the DSN writes to the file, so a
		// file that migrate refuses is left byte for byte as it was.
		q := url.Values{}
		q.Add("_pragma", "foreign_keys(1)")
		q.Add("_pragma", "busy_timeout(10000)")
		q.Set("_txlock", "immediate")
		dsn = "file:" + (&url.URL{Path: path}).EscapedPath() + "?" + q.Encode()
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: every pragma applies, ":memory:" stays one database, and writes
	// from the web handlers and the print workers serialise without SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path, Now: time.Now,
		HistoryLimit: DefaultHistoryLimit, CrateNameMax: DefaultCrateNameMax, CrateRecordsMax: DefaultCrateRecordsMax,
		CacheMaxAge: DefaultCacheMaxAge, CacheMaxBytes: DefaultCacheMaxBytes,
		ImageMaxAge: DefaultImageMaxAge, ImageMaxBytes: DefaultImageMaxBytes,
		TempoMaxAge: DefaultTempoMaxAge, TempoMaxBytes: DefaultTempoMaxBytes}
	ctx := context.Background()
	if err := s.migrate(ctx, migrations); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if path != ":memory:" {
		// WAL lets the CLI read while the server writes. The mode is stored in the
		// file, so it is set once the file is known to be ours.
		if _, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
			db.Close()
			return nil, fmt.Errorf("open %s: %w", path, err)
		}
	}
	if err := s.Prune(ctx); err != nil {
		s.Log("could not prune the caches", err)
	}
	return s, nil
}

// migrate brings the database to len(steps), applying the missing steps in order in
// one transaction. A file that is not ours, or is newer than this build, is refused
// and left untouched.
func (s *Store) migrate(ctx context.Context, steps []string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var version, appID, tables int
		if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, "PRAGMA application_id").Scan(&appID); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type = 'table'").Scan(&tables); err != nil {
			return err
		}
		fresh := version == 0 && appID == 0 && tables == 0
		if !fresh && appID != applicationID {
			return errors.New("this file is not a crate-labels database, or was written by a pre-release build. " +
				"Move it aside to start afresh")
		}
		if version > len(steps) {
			return fmt.Errorf("this database was written by a newer crate-labels (schema %d; this build knows %d). "+
				"Update crate-labels, or move the file aside to start afresh", version, len(steps))
		}
		for v := version + 1; v <= len(steps); v++ {
			if _, err := tx.ExecContext(ctx, steps[v-1]); err != nil {
				return fmt.Errorf("schema step %d: %w", v, err)
			}
		}
		if version == len(steps) {
			return nil
		}
		_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id = %d; PRAGMA user_version = %d", applicationID, len(steps)))
		return err
	})
}

// SchemaVersionOf reports the database's PRAGMA user_version.
func (s *Store) SchemaVersionOf(ctx context.Context) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

// Log reports a background store problem that does not fail the caller.
func (s *Store) Log(msg string, err error) { slog.Warn(msg, "err", err) }

// Path is the database file (":memory:" for in-memory).
func (s *Store) Path() string { return s.path }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) nowMS() int64 { return s.Now().UnixMilli() }

// tx runs fn in one transaction. fn must use only tx: the pool has a single connection.
func (s *Store) tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func dumps(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

func loadMap(text sql.NullString) map[string]any {
	out := map[string]any{}
	if text.Valid && text.String != "" {
		_ = json.Unmarshal([]byte(text.String), &out)
	}
	if out == nil {
		out = map[string]any{}
	}
	return out
}

func nullInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	x := v.Int64
	return &x
}

// --- settings ---------------------------------------------------------------------------

// Setting returns the raw JSON value of key and whether it exists.
func (s *Store) Setting(ctx context.Context, key string) (json.RawMessage, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return json.RawMessage(v), true, nil
}

// SettingString returns key as a string, or "" when it is missing or not a string.
func (s *Store) SettingString(ctx context.Context, key string) string {
	raw, ok, err := s.Setting(ctx, key)
	if err != nil || !ok {
		return ""
	}
	var out string
	if json.Unmarshal(raw, &out) != nil {
		return ""
	}
	return out
}

// SettingBool returns key as a bool, or false when it is missing or not a bool.
func (s *Store) SettingBool(ctx context.Context, key string) bool {
	raw, ok, err := s.Setting(ctx, key)
	if err != nil || !ok {
		return false
	}
	var out bool
	if json.Unmarshal(raw, &out) != nil {
		return false
	}
	return out
}

// SetSetting stores value as JSON under key. A nil value deletes the key.
func (s *Store) SetSetting(ctx context.Context, key string, value any) error {
	if value == nil {
		_, err := s.db.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", key)
		return err
	}
	if raw, ok := value.(json.RawMessage); ok && (len(raw) == 0 || string(raw) == "null") {
		_, err := s.db.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", key)
		return err
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		"INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		key, string(b))
	return err
}

// Settings returns every setting as raw JSON.
func (s *Store) Settings(ctx context.Context) (map[string]json.RawMessage, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key, value FROM settings ORDER BY key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = json.RawMessage(v)
	}
	return out, rows.Err()
}

// --- Discogs identity (no secret here) -----------------------------------------------------

// DiscogsAccount returns the username of the last successful identity check and where the
// token is kept. ok is false when no account is recorded.
func (s *Store) DiscogsAccount(ctx context.Context) (username, tokenRef string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT username, token_ref FROM discogs_identity WHERE id = 1").
		Scan(&username, &tokenRef)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return username, tokenRef, err == nil, err
}

// SetDiscogsAccount records the account a token belongs to.
func (s *Store) SetDiscogsAccount(ctx context.Context, username, tokenRef string) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO discogs_identity (id, username, token_ref, checked_at) VALUES (1, ?, ?, ?) "+
			"ON CONFLICT(id) DO UPDATE SET username = excluded.username, token_ref = excluded.token_ref, "+
			"checked_at = excluded.checked_at",
		username, tokenRef, s.nowMS())
	return err
}

// ClearDiscogsAccount forgets the account.
func (s *Store) ClearDiscogsAccount(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM discogs_identity")
	return err
}

// --- response and image caches ---------------------------------------------------------------

// CacheGet returns a cached response body no older than maxAge.
func (s *Store) CacheGet(ctx context.Context, url string, maxAge time.Duration) ([]byte, bool, error) {
	var body string
	var fetched int64
	err := s.db.QueryRowContext(ctx, "SELECT body, fetched_at FROM response_cache WHERE url = ?", url).Scan(&body, &fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if s.nowMS()-fetched > maxAge.Milliseconds() {
		return nil, false, nil
	}
	return []byte(body), true, nil
}

// CachePut stores a response body under url.
func (s *Store) CachePut(ctx context.Context, url string, body []byte) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO response_cache (url, body, fetched_at) VALUES (?, ?, ?) "+
			"ON CONFLICT(url) DO UPDATE SET body = excluded.body, fetched_at = excluded.fetched_at",
		url, string(body), s.nowMS())
	s.maybePrune(ctx)
	return err
}

// ImageGet returns a cached image.
func (s *Store) ImageGet(ctx context.Context, url string) (contentType string, body []byte, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT content_type, body FROM image_cache WHERE url = ?", url).
		Scan(&contentType, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, false, nil
	}
	return contentType, body, err == nil, err
}

// ImagePut caches an image.
func (s *Store) ImagePut(ctx context.Context, url, contentType string, body []byte) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT OR REPLACE INTO image_cache (url, content_type, body, fetched_at) VALUES (?, ?, ?, ?)",
		url, contentType, body, s.nowMS())
	s.maybePrune(ctx)
	return err
}

// maybePrune prunes the caches every pruneEvery writes.
func (s *Store) maybePrune(ctx context.Context) {
	if s.puts.Add(1)%pruneEvery != 0 {
		return
	}
	if err := s.Prune(ctx); err != nil {
		s.Log("could not prune the caches", err)
	}
}

// Prune keeps the response, image and tempo caches bounded: entries older than the max
// age go, then the oldest entries until the bodies fit in the max bytes.
func (s *Store) Prune(ctx context.Context) error {
	now := s.nowMS()
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, c := range []struct {
			table, key, body, at string
			age                  time.Duration
			bytes                int64
		}{
			{"response_cache", "url", "body", "fetched_at", s.CacheMaxAge, s.CacheMaxBytes},
			{"image_cache", "url", "body", "fetched_at", s.ImageMaxAge, s.ImageMaxBytes},
			{"tempo_cache", "key", "result_json", "created_at", s.TempoMaxAge, s.TempoMaxBytes},
		} {
			if c.age > 0 {
				if _, err := tx.ExecContext(ctx, "DELETE FROM "+c.table+" WHERE "+c.at+" < ?", now-c.age.Milliseconds()); err != nil {
					return err
				}
			}
			if c.bytes > 0 {
				// Keep the newest entries whose bodies add up to at most c.bytes.
				if _, err := tx.ExecContext(ctx, "DELETE FROM "+c.table+" WHERE "+c.key+" IN (SELECT "+c.key+" FROM "+
					"(SELECT "+c.key+", SUM(length(CAST("+c.body+" AS BLOB))) OVER (ORDER BY "+c.at+" DESC, "+c.key+") AS kept FROM "+c.table+") "+
					"WHERE kept > ?)", c.bytes); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
