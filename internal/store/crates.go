package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// untitledCrate is the name a crate gets when none is given. Further unnamed crates are
// numbered from 2 ("Untitled crate 2"), because crate names are unique.
const untitledCrate = "Untitled crate"

// Crate is a named, ordered list of records whose labels print together.
type Crate struct {
	ID        string
	Name      string
	CreatedAt int64
	Records   int // how many records it holds
}

// CrateRecord is one record in a crate. Artist, Title, Catno, Year and Thumb are what the
// crate list shows without asking Discogs; Data is the edited label, if one was saved.
type CrateRecord struct {
	ID        int64
	CrateID   string
	Position  int
	ReleaseID int64
	Artist    string
	Title     string
	Catno     string
	Year      *int64 // nil = unknown
	Thumb     string // "" = none
	Data      json.RawMessage
	EditedAt  *int64 // when Data was last saved
	AddedAt   int64
}

// NewCrateRecord is what AddToCrate stores.
type NewCrateRecord struct {
	ReleaseID                   int64
	Artist, Title, Catno, Thumb string
	Year                        *int64
	Data                        json.RawMessage // nil = no edited label
}

const crateCols = "c.id, c.name, c.created_at, (SELECT count(*) FROM crate_records r WHERE r.crate_id = c.id)"

func scanCrate(scan func(...any) error) (Crate, error) {
	var c Crate
	err := scan(&c.ID, &c.Name, &c.CreatedAt, &c.Records)
	return c, err
}

const recordCols = "id, crate_id, position, release_id, artist, title, catno, year, thumb, data_json, edited_at, added_at"

func scanRecord(scan func(...any) error) (CrateRecord, error) {
	var r CrateRecord
	var year, edited sql.NullInt64
	var data sql.NullString
	err := scan(&r.ID, &r.CrateID, &r.Position, &r.ReleaseID, &r.Artist, &r.Title, &r.Catno, &year, &r.Thumb,
		&data, &edited, &r.AddedAt)
	r.Year, r.EditedAt = nullInt(year), nullInt(edited)
	if data.Valid && data.String != "" {
		r.Data = json.RawMessage(data.String)
	}
	return r, err
}

// Crates lists crates, oldest first.
func (s *Store) Crates(ctx context.Context) ([]Crate, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+crateCols+" FROM crates c ORDER BY c.created_at, c.rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Crate{}
	for rows.Next() {
		c, err := scanCrate(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Crate returns one crate.
func (s *Store) Crate(ctx context.Context, id string) (Crate, error) {
	c, err := scanCrate(s.db.QueryRowContext(ctx, "SELECT "+crateCols+" FROM crates c WHERE c.id = ?", id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return c, notFound("No crate %q.", id)
	}
	return c, err
}

// nameTaken reports whether another crate (not except) already uses name, ignoring case.
// The comparison is done here rather than in SQL because SQLite's NOCASE folds only
// ASCII letters; the column's own NOCASE constraint is a backstop.
func nameTaken(ctx context.Context, tx *sql.Tx, name, except string) (bool, error) {
	rows, err := tx.QueryContext(ctx, "SELECT name FROM crates WHERE id != ?", except)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var other string
		if err := rows.Scan(&other); err != nil {
			return false, err
		}
		if strings.EqualFold(other, name) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// checkName tidies name's spaces and enforces the rules: not blank, at most
// CrateNameMax characters, and not used by another crate (except is the crate being
// renamed).
func (s *Store) checkName(ctx context.Context, tx *sql.Tx, name, except string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "", invalid("Give the crate a name.")
	}
	if utf8.RuneCountInString(name) > s.CrateNameMax {
		return "", invalid("Crate names are at most %d characters.", s.CrateNameMax)
	}
	taken, err := nameTaken(ctx, tx, name, except)
	if err != nil {
		return "", err
	}
	if taken {
		return "", invalid("There is already a crate called %q.", name)
	}
	return name, nil
}

// untitledName is the first free "Untitled crate", "Untitled crate 2", ….
func untitledName(ctx context.Context, tx *sql.Tx) (string, error) {
	for n := 1; ; n++ {
		name := untitledCrate
		if n > 1 {
			name = fmt.Sprintf("%s %d", untitledCrate, n)
		}
		taken, err := nameTaken(ctx, tx, name, "")
		if err != nil || !taken {
			return name, err
		}
	}
}

// CreateCrate makes an empty crate. A blank name becomes the first free "Untitled crate".
func (s *Store) CreateCrate(ctx context.Context, name string) (Crate, error) {
	id := NewID(6)
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		if strings.TrimSpace(name) == "" {
			name, err = untitledName(ctx, tx)
		} else {
			name, err = s.checkName(ctx, tx, name, "")
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO crates (id, name, created_at) VALUES (?, ?, ?)", id, name, s.nowMS())
		return err
	})
	if err != nil {
		return Crate{}, err
	}
	return s.Crate(ctx, id)
}

// RenameCrate renames a crate. The new name must not be blank or taken.
func (s *Store) RenameCrate(ctx context.Context, id, name string) (Crate, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		name, err := s.checkName(ctx, tx, name, id)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "UPDATE crates SET name = ? WHERE id = ?", name, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFound("No crate %q.", id)
		}
		return nil
	})
	if err != nil {
		return Crate{}, err
	}
	return s.Crate(ctx, id)
}

// DeleteCrate removes a crate and its records.
func (s *Store) DeleteCrate(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM crates WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFound("No crate %q.", id)
	}
	return nil
}

// CrateRecords lists a crate's records in crate order.
func (s *Store) CrateRecords(ctx context.Context, crateID string) ([]CrateRecord, error) {
	if _, err := s.Crate(ctx, crateID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+recordCols+" FROM crate_records WHERE crate_id = ? ORDER BY position, id", crateID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CrateRecord{}
	for rows.Next() {
		r, err := scanRecord(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CrateRecord returns one crate record.
func (s *Store) CrateRecord(ctx context.Context, id int64) (CrateRecord, error) {
	r, err := scanRecord(s.db.QueryRowContext(ctx, "SELECT "+recordCols+" FROM crate_records WHERE id = ?", id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return r, notFound("No crate record %d.", id)
	}
	return r, err
}

// AddToCrate appends a record to the end of a crate. A crate holds each release once and
// at most CrateRecordsMax records.
func (s *Store) AddToCrate(ctx context.Context, crateID string, n NewCrateRecord) (CrateRecord, error) {
	var data any
	if len(n.Data) > 0 {
		data = string(n.Data)
	}
	var id int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		// The checks and the insert share one (immediate) transaction, so two processes
		// adding at once cannot overfill a crate.
		c, err := scanCrate(tx.QueryRowContext(ctx, "SELECT "+crateCols+" FROM crates c WHERE c.id = ?", crateID).Scan)
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("No crate %q.", crateID)
		}
		if err != nil {
			return err
		}
		if c.Records >= s.CrateRecordsMax {
			return invalid("%s is full: a crate holds up to %d records.", c.Name, s.CrateRecordsMax)
		}
		var dup int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM crate_records WHERE crate_id = ? AND release_id = ?",
			crateID, n.ReleaseID).Scan(&dup); err != nil {
			return err
		}
		if dup > 0 {
			return invalid("That record is already in %s.", c.Name)
		}
		var pos int
		if err := tx.QueryRowContext(ctx, "SELECT coalesce(max(position), -1) + 1 FROM crate_records WHERE crate_id = ?",
			crateID).Scan(&pos); err != nil {
			return err
		}
		var edited any
		if data != nil {
			edited = s.nowMS()
		}
		res, err := tx.ExecContext(ctx,
			"INSERT INTO crate_records (crate_id, position, release_id, artist, title, catno, year, thumb, data_json, "+
				"edited_at, added_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			crateID, pos, n.ReleaseID, n.Artist, n.Title, n.Catno, n.Year, n.Thumb, data, edited, s.nowMS())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return CrateRecord{}, err
	}
	return s.CrateRecord(ctx, id)
}

// SetCrateRecordData saves an edited label for a crate record; nil or empty data clears
// it, so the record prints from Discogs again.
func (s *Store) SetCrateRecordData(ctx context.Context, id int64, data json.RawMessage) (CrateRecord, error) {
	var value, edited any
	if len(data) > 0 {
		value, edited = string(data), s.nowMS()
	}
	res, err := s.db.ExecContext(ctx, "UPDATE crate_records SET data_json = ?, edited_at = ? WHERE id = ?", value, edited, id)
	if err != nil {
		return CrateRecord{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return CrateRecord{}, notFound("No crate record %d.", id)
	}
	return s.CrateRecord(ctx, id)
}

// RemoveFromCrate deletes a crate record.
func (s *Store) RemoveFromCrate(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM crate_records WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFound("No crate record %d.", id)
	}
	return nil
}

// ReorderCrate sets the crate order from ids, which must name every record in the crate
// exactly once.
func (s *Store) ReorderCrate(ctx context.Context, crateID string, ids []int64) ([]CrateRecord, error) {
	records, err := s.CrateRecords(ctx, crateID)
	if err != nil {
		return nil, err
	}
	current := map[int64]bool{}
	for _, r := range records {
		current[r.ID] = true
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		if !current[id] || seen[id] {
			return nil, invalid("The new order must name every record in the crate exactly once.")
		}
		seen[id] = true
	}
	if len(seen) != len(current) {
		return nil, invalid("The new order must name every record in the crate exactly once.")
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		for pos, id := range ids {
			if _, err := tx.ExecContext(ctx, "UPDATE crate_records SET position = ? WHERE id = ?", pos, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.CrateRecords(ctx, crateID)
}
