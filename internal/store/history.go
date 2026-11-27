package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
)

// HistoryEntry is one label in the reprint history: the full label data plus a few of
// its fields for the list.
type HistoryEntry struct {
	ID        int64
	PrintedAt int64
	Template  string
	Stock     string
	Printer   string // printer name
	Artist    string
	Title     string
	Catno     string
	Year      string
	Label     string // record label
	Format    string
	ReleaseID *int64
	Copies    int
	Options   map[string]any
	Data      json.RawMessage // the label data, in canonical form
}

// NewHistoryEntry is what AddHistory stores.
type NewHistoryEntry struct {
	Data     json.RawMessage // label data JSON, already in canonical form
	Template string
	Stock    string
	Printer  string
	Copies   int
	Options  map[string]any
}

// field reads a label data field as text.
func field(data map[string]any, key string) string {
	switch v := data[key].(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}

// AddHistory puts a label at the top of the history. An older entry for the same data,
// template, stock and options is replaced rather than repeated, and only the newest
// HistoryLimit entries are kept.
func (s *Store) AddHistory(ctx context.Context, n NewHistoryEntry) (HistoryEntry, error) {
	if n.Copies < 1 {
		n.Copies = 1
	}
	if n.Options == nil {
		n.Options = map[string]any{}
	}
	if len(n.Data) == 0 {
		n.Data = json.RawMessage("{}")
	}
	var data map[string]any
	if err := json.Unmarshal(n.Data, &data); err != nil {
		return HistoryEntry{}, fmt.Errorf("history: label data is not a JSON object: %w", err)
	}
	optionsJSON := dumps(n.Options)
	sameAs := dumps([]any{string(n.Data), n.Template, n.Stock, optionsJSON})
	var releaseID any
	if f, ok := data["release_id"].(float64); ok && f == math.Trunc(f) && f > 0 {
		releaseID = int64(f)
	}
	var id int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM print_history WHERE same_as = ?", sameAs); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			"INSERT INTO print_history (printed_at, template, stock, printer, artist, title, catno, year, label, "+
				"format, release_id, copies, options_json, data_json, same_as) "+
				"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			s.nowMS(), n.Template, n.Stock, n.Printer, field(data, "artist"), field(data, "title"),
			field(data, "catno"), field(data, "year"), field(data, "label"), field(data, "format"),
			releaseID, n.Copies, optionsJSON, string(n.Data), sameAs)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			"DELETE FROM print_history WHERE id NOT IN "+
				"(SELECT id FROM print_history ORDER BY printed_at DESC, id DESC LIMIT ?)", s.HistoryLimit)
		return err
	})
	if err != nil {
		return HistoryEntry{}, err
	}
	return s.HistoryEntry(ctx, id)
}

const historyCols = "id, printed_at, template, stock, printer, artist, title, catno, year, label, format, " +
	"release_id, copies, options_json, data_json"

func scanHistory(scan func(...any) error) (HistoryEntry, error) {
	var h HistoryEntry
	var opts sql.NullString
	var rid sql.NullInt64
	var data string
	err := scan(&h.ID, &h.PrintedAt, &h.Template, &h.Stock, &h.Printer, &h.Artist, &h.Title, &h.Catno, &h.Year,
		&h.Label, &h.Format, &rid, &h.Copies, &opts, &data)
	h.ReleaseID = nullInt(rid)
	h.Options = loadMap(opts)
	h.Data = json.RawMessage(data)
	if len(h.Data) == 0 {
		h.Data = json.RawMessage("{}")
	}
	return h, err
}

// History lists entries newest first.
func (s *Store) History(ctx context.Context, limit int) ([]HistoryEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+historyCols+" FROM print_history ORDER BY printed_at DESC, id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		h, err := scanHistory(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// HistoryEntry returns one entry.
func (s *Store) HistoryEntry(ctx context.Context, id int64) (HistoryEntry, error) {
	h, err := scanHistory(s.db.QueryRowContext(ctx, "SELECT "+historyCols+" FROM print_history WHERE id = ?", id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return h, notFound("No history entry %d.", id)
	}
	return h, err
}

// DeleteHistory removes one entry. It does not change whether the record counts as
// printed; that comes from the label-print log.
func (s *Store) DeleteHistory(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM print_history WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFound("No history entry %d.", id)
	}
	return nil
}
