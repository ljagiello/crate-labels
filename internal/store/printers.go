package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// NewID returns n random bytes as lower-case hex (2n characters).
func NewID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func scanPrinter(scan func(...any) error) (printer.Target, error) {
	var t printer.Target
	var settings sql.NullString
	if err := scan(&t.ID, &t.Name, &t.ModelID, &t.Address, &settings); err != nil {
		return t, err
	}
	t.Settings = printer.Settings(loadMap(settings))
	return t, nil
}

const printerCols = "id, name, model_id, address, settings_json"

// Printers lists saved printers, most recently used first, then by name.
func (s *Store) Printers(ctx context.Context) ([]printer.Target, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+printerCols+" FROM printers ORDER BY last_used_at IS NULL, last_used_at DESC, name COLLATE NOCASE, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []printer.Target{}
	for rows.Next() {
		t, err := scanPrinter(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Printer returns a saved printer by id.
func (s *Store) Printer(ctx context.Context, id string) (printer.Target, error) {
	t, err := scanPrinter(s.db.QueryRowContext(ctx, "SELECT "+printerCols+" FROM printers WHERE id = ?", id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return t, notFound("No saved printer %q.", id)
	}
	return t, err
}

// FindPrinter returns a saved printer by id, or by name ignoring case.
func (s *Store) FindPrinter(ctx context.Context, idOrName string) (printer.Target, error) {
	t, err := scanPrinter(s.db.QueryRowContext(ctx,
		"SELECT "+printerCols+" FROM printers WHERE id = ? OR lower(name) = lower(?) ORDER BY id = ? DESC LIMIT 1",
		idOrName, idOrName, idOrName).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return t, notFound("No saved printer called %q.", idOrName)
	}
	return t, err
}

// SavePrinter inserts or updates t. An empty t.ID gets a new 12-hex-character id.
func (s *Store) SavePrinter(ctx context.Context, t *printer.Target) error {
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" {
		return invalid("A printer needs a name.")
	}
	if t.ID == "" {
		t.ID = NewID(6)
	}
	if t.Settings == nil {
		t.Settings = printer.Settings{}
	}
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO printers (id, name, model_id, address, settings_json, created_at) VALUES (?, ?, ?, ?, ?, ?) "+
			"ON CONFLICT(id) DO UPDATE SET name = excluded.name, model_id = excluded.model_id, "+
			"address = excluded.address, settings_json = excluded.settings_json",
		t.ID, t.Name, t.ModelID, t.Address, dumps(map[string]any(t.Settings)), s.nowMS())
	return err
}

// TouchPrinter marks a printer as just used.
func (s *Store) TouchPrinter(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE printers SET last_used_at = ? WHERE id = ?", s.nowMS(), id)
	return err
}

// DeletePrinter removes a saved printer.
func (s *Store) DeletePrinter(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM printers WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFound("No saved printer %q.", id)
	}
	return nil
}
