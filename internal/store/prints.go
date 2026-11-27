package store

import (
	"context"
	"database/sql"
	"slices"
	"strings"
)

// Printed overrides: the user's word on whether a release has a label.
const (
	OverrideAuto       = "auto"        // no override: the label-print log decides
	OverridePrinted    = "printed"     // it has a label, whatever the log says
	OverrideNotPrinted = "not_printed" // it has no label (one fell off, or was thrown away)
)

// LabelPrint is one record a job printed.
type LabelPrint struct {
	ReleaseID int64 // 0 = no Discogs release (typed by hand, or a test print)
	JobID     int64 // 0 = not from a job
	Template  string
	Copies    int
}

// PrintedState says whether a release has a label, and why.
type PrintedState struct {
	Printed       bool
	Override      string // OverrideAuto, OverridePrinted or OverrideNotPrinted
	Labels        int    // labels printed for it, from the log
	LastPrintedAt *int64 // nil when it was never printed
}

// derive applies the override; auto means "printed once any label for it printed".
func (p *PrintedState) derive() {
	switch p.Override {
	case OverridePrinted:
		p.Printed = true
	case OverrideNotPrinted:
		p.Printed = false
	default:
		p.Override = OverrideAuto
		p.Printed = p.Labels > 0
	}
}

// LogPrints records what a job printed, in one transaction. Printing a release again
// lifts a "not printed" override on it: it has a label now.
func (s *Store) LogPrints(ctx context.Context, prints []LabelPrint) error {
	now := s.nowMS()
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, p := range prints {
			if p.Copies < 1 {
				continue
			}
			var rid, job any
			if p.ReleaseID > 0 {
				rid = p.ReleaseID
			}
			if p.JobID > 0 {
				job = p.JobID
			}
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO label_prints (release_id, job_id, template, copies, printed_at) VALUES (?, ?, ?, ?, ?)",
				rid, job, p.Template, p.Copies, now); err != nil {
				return err
			}
			if rid != nil {
				if _, err := tx.ExecContext(ctx,
					"DELETE FROM printed_overrides WHERE release_id = ? AND state = ?", rid, OverrideNotPrinted); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// LabelsPrinted is how many labels were ever printed.
func (s *Store) LabelsPrinted(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT coalesce(sum(copies), 0) FROM label_prints").Scan(&n)
	return n, err
}

// sqliteMaxArgs keeps each IN (...) list well under SQLite's bound-parameter limit.
const sqliteMaxArgs = 500

// PrintedStates returns the state of every release in ids (ids ≤ 0 are skipped).
func (s *Store) PrintedStates(ctx context.Context, ids []int64) (map[int64]PrintedState, error) {
	out := map[int64]PrintedState{}
	var want []int64
	for _, id := range ids {
		if _, dup := out[id]; id > 0 && !dup {
			out[id] = PrintedState{}
			want = append(want, id)
		}
	}
	for chunk := range slices.Chunk(want, sqliteMaxArgs) {
		in := "(" + strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",") + ")"
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		if err := s.scanStates(ctx, out,
			"SELECT release_id, sum(copies), max(printed_at) FROM label_prints WHERE release_id IN "+in+" GROUP BY release_id",
			args, func(st *PrintedState, scan func(...any) error) error {
				var last int64
				if err := scan(&st.Labels, &last); err != nil {
					return err
				}
				st.LastPrintedAt = &last
				return nil
			}); err != nil {
			return nil, err
		}
		if err := s.scanStates(ctx, out, "SELECT release_id, state FROM printed_overrides WHERE release_id IN "+in,
			args, func(st *PrintedState, scan func(...any) error) error { return scan(&st.Override) }); err != nil {
			return nil, err
		}
	}
	for id, st := range out {
		st.derive()
		out[id] = st
	}
	return out, nil
}

// scanStates runs query and lets fill read the columns after release_id into out[id].
func (s *Store) scanStates(ctx context.Context, out map[int64]PrintedState, query string, args []any,
	fill func(st *PrintedState, scan func(...any) error) error) error {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var st PrintedState
		err := fill(&st, func(dst ...any) error { return rows.Scan(append([]any{&id}, dst...)...) })
		if err != nil {
			return err
		}
		cur := out[id]
		if st.Labels > 0 {
			cur.Labels, cur.LastPrintedAt = st.Labels, st.LastPrintedAt
		}
		if st.Override != "" {
			cur.Override = st.Override
		}
		out[id] = cur
	}
	return rows.Err()
}

// PrintedState returns one release's state.
func (s *Store) PrintedState(ctx context.Context, releaseID int64) (PrintedState, error) {
	states, err := s.PrintedStates(ctx, []int64{releaseID})
	if err != nil {
		return PrintedState{}, err
	}
	st, ok := states[releaseID]
	if !ok {
		st.derive()
	}
	return st, nil
}

// SetPrintedOverride sets a release's override: OverrideAuto removes it.
func (s *Store) SetPrintedOverride(ctx context.Context, releaseID int64, override string) (PrintedState, error) {
	if releaseID <= 0 {
		return PrintedState{}, invalid("Give a Discogs release id.")
	}
	var err error
	switch override {
	case OverrideAuto:
		_, err = s.db.ExecContext(ctx, "DELETE FROM printed_overrides WHERE release_id = ?", releaseID)
	case OverridePrinted, OverrideNotPrinted:
		_, err = s.db.ExecContext(ctx,
			"INSERT INTO printed_overrides (release_id, state, set_at) VALUES (?, ?, ?) "+
				"ON CONFLICT(release_id) DO UPDATE SET state = excluded.state, set_at = excluded.set_at",
			releaseID, override, s.nowMS())
	default:
		return PrintedState{}, invalid("The override is auto, printed or not_printed.")
	}
	if err != nil {
		return PrintedState{}, err
	}
	return s.PrintedState(ctx, releaseID)
}
