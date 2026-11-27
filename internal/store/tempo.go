package store

import (
	"context"
	"database/sql"
	"errors"
)

// TempoGet returns the cached tempo analysis (result JSON) under key.
func (s *Store) TempoGet(ctx context.Context, key string) ([]byte, bool, error) {
	var body string
	err := s.db.QueryRowContext(ctx, "SELECT result_json FROM tempo_cache WHERE key = ?", key).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return []byte(body), true, nil
}

// TempoPut caches a tempo analysis (result JSON) under key.
func (s *Store) TempoPut(ctx context.Context, key string, resultJSON []byte) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO tempo_cache (key, result_json, created_at) VALUES (?, ?, ?) "+
			"ON CONFLICT(key) DO UPDATE SET result_json = excluded.result_json, created_at = excluded.created_at",
		key, string(resultJSON), s.nowMS())
	s.maybePrune(ctx)
	return err
}
