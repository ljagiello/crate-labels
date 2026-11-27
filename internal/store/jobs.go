package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

// Job statuses.
const (
	JobQueued      = "queued"
	JobPrinting    = "printing"
	JobDone        = "done"
	JobFailed      = "failed"
	JobCancelled   = "cancelled"
	JobInterrupted = "interrupted"
)

// InterruptedMessage is the error a job left unfinished by a previous server gets.
const InterruptedMessage = "crate-labels stopped before this job finished"

var jobStatuses = []string{JobQueued, JobPrinting, JobDone, JobFailed, JobCancelled, JobInterrupted}

// Active reports whether a status means the job is still waiting or running.
func Active(status string) bool { return status == JobQueued || status == JobPrinting }

// Job is one print job: every record, copies times, in one driver call.
type Job struct {
	ID           int64
	CreatedAt    int64
	StartedAt    *int64
	FinishedAt   *int64
	Status       string
	PrinterID    string
	PrinterName  string
	Template     string
	Stock        string
	Options      map[string]any    // template options
	PrintOptions map[string]any    // per-job driver settings
	Records      []json.RawMessage // each record's label data, frozen when the job was queued
	Copies       int               // per record
	Total        int               // records × copies
	Printed      int
	Progress     float64
	Message      string
	Error        string // "" = none
	Source       string
	Owner        string // the process that queued it, "<pid>:<start>"
}

// NewJob is what CreateJob stores.
type NewJob struct {
	PrinterID, PrinterName, Template, Stock string
	Records                                 []json.RawMessage
	Copies                                  int
	Options, PrintOptions                   map[string]any
	Source                                  string
	Owner                                   string
}

// JobUpdate changes a job. Nil fields are left alone. Moving to "printing" sets
// started_at; moving to a final status sets finished_at.
type JobUpdate struct {
	Status   *string
	Printed  *int
	Progress *float64
	Message  *string
	Error    *string
}

const jobCols = "id, created_at, started_at, finished_at, status, printer_id, printer_name, template, stock, " +
	"options_json, print_options_json, records_json, copies, total, printed, progress, message, error, source, owner"

func scanJob(scan func(...any) error) (Job, error) {
	var j Job
	var started, finished sql.NullInt64
	var opts, popts, errText sql.NullString
	var records string
	err := scan(&j.ID, &j.CreatedAt, &started, &finished, &j.Status, &j.PrinterID, &j.PrinterName, &j.Template,
		&j.Stock, &opts, &popts, &records, &j.Copies, &j.Total, &j.Printed, &j.Progress, &j.Message, &errText, &j.Source, &j.Owner)
	if err != nil {
		return j, err
	}
	j.StartedAt, j.FinishedAt = nullInt(started), nullInt(finished)
	j.Options, j.PrintOptions = loadMap(opts), loadMap(popts)
	j.Error = errText.String
	if err := json.Unmarshal([]byte(records), &j.Records); err != nil || j.Records == nil {
		j.Records = []json.RawMessage{}
	}
	return j, nil
}

// CreateJob queues a job.
func (s *Store) CreateJob(ctx context.Context, n NewJob) (Job, error) {
	if n.Copies < 1 {
		n.Copies = 1
	}
	if n.Options == nil {
		n.Options = map[string]any{}
	}
	if n.PrintOptions == nil {
		n.PrintOptions = map[string]any{}
	}
	records := n.Records
	if records == nil {
		records = []json.RawMessage{}
	}
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO print_jobs (created_at, status, printer_id, printer_name, template, stock, options_json, "+
			"print_options_json, records_json, copies, total, source, owner) VALUES (?, 'queued', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		s.nowMS(), n.PrinterID, n.PrinterName, n.Template, n.Stock, dumps(n.Options), dumps(n.PrintOptions),
		dumps(records), n.Copies, len(records)*n.Copies, n.Source, n.Owner)
	if err != nil {
		return Job{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Job{}, err
	}
	return s.Job(ctx, id)
}

// Job returns one job.
func (s *Store) Job(ctx context.Context, id int64) (Job, error) {
	j, err := scanJob(s.db.QueryRowContext(ctx, "SELECT "+jobCols+" FROM print_jobs WHERE id = ?", id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return j, notFound("No print job %d.", id)
	}
	return j, err
}

// Jobs lists jobs newest first. activeOnly keeps queued and printing jobs.
func (s *Store) Jobs(ctx context.Context, limit int, activeOnly bool) ([]Job, error) {
	where := ""
	if activeOnly {
		where = "WHERE status IN ('queued', 'printing') "
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+jobCols+" FROM print_jobs "+where+"ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// UpdateJob applies u and returns the job as stored.
func (s *Store) UpdateJob(ctx context.Context, id int64, u JobUpdate) (Job, error) {
	var sets []string
	var args []any
	if u.Status != nil {
		if !slices.Contains(jobStatuses, *u.Status) {
			return Job{}, invalid("Unknown job status %q.", *u.Status)
		}
		sets = append(sets, "status = ?")
		args = append(args, *u.Status)
		switch {
		case *u.Status == JobPrinting:
			sets = append(sets, "started_at = ?")
			args = append(args, s.nowMS())
		case !Active(*u.Status):
			sets = append(sets, "finished_at = ?")
			args = append(args, s.nowMS())
		}
	}
	if u.Printed != nil {
		sets = append(sets, "printed = ?")
		args = append(args, *u.Printed)
	}
	if u.Progress != nil {
		sets = append(sets, "progress = ?")
		args = append(args, *u.Progress)
	}
	if u.Message != nil {
		sets = append(sets, "message = ?")
		args = append(args, *u.Message)
	}
	if u.Error != nil {
		sets = append(sets, "error = ?")
		args = append(args, *u.Error)
	}
	if len(sets) > 0 {
		args = append(args, id)
		if _, err := s.db.ExecContext(ctx, "UPDATE print_jobs SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...); err != nil {
			return Job{}, err
		}
	}
	return s.Job(ctx, id)
}

// InterruptStaleJobs marks queued or printing jobs as interrupted when stale(owner)
// says their owner is gone, and returns their ids. Interrupted jobs never resume on
// their own. The check and the update share one transaction.
func (s *Store) InterruptStaleJobs(ctx context.Context, stale func(owner string) bool) ([]int64, error) {
	var ids []int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id, owner FROM print_jobs WHERE status IN ('queued', 'printing') ORDER BY id")
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			var owner string
			if err := rows.Scan(&id, &owner); err != nil {
				rows.Close()
				return err
			}
			if stale(owner) {
				ids = append(ids, id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx,
				"UPDATE print_jobs SET status = 'interrupted', finished_at = ?, error = coalesce(error, ?) WHERE id = ?",
				s.nowMS(), InterruptedMessage, id); err != nil {
				return err
			}
		}
		return nil
	})
	return ids, err
}

// TransitionJob moves a job from status from to status to, setting message, only if it
// is still in from. ok is false when another writer moved it first, which makes the
// queued→printing and queued→cancelled races safe.
func (s *Store) TransitionJob(ctx context.Context, id int64, from, to, message string) (Job, bool, error) {
	if !slices.Contains(jobStatuses, to) {
		return Job{}, false, invalid("Unknown job status %q.", to)
	}
	set := "status = ?, message = ?"
	args := []any{to, message}
	switch {
	case to == JobPrinting:
		set += ", started_at = ?"
		args = append(args, s.nowMS())
	case !Active(to):
		set += ", finished_at = ?"
		args = append(args, s.nowMS())
	}
	args = append(args, id, from)
	res, err := s.db.ExecContext(ctx, "UPDATE print_jobs SET "+set+" WHERE id = ? AND status = ?", args...)
	if err != nil {
		return Job{}, false, err
	}
	n, _ := res.RowsAffected()
	j, err := s.Job(ctx, id)
	return j, n == 1 && err == nil, err
}
