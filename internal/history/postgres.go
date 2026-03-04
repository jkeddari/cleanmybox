package history

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, strings.TrimSpace(databaseURL))
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}

	store := &PostgresStore{pool: pool}
	if err := store.ensureSchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}

	return store, nil
}

func (s *PostgresStore) SaveRun(ctx context.Context, record RunRecord) error {
	statsJSON, err := json.Marshal(record.Stats)
	if err != nil {
		return err
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO cleanup_history (
			checkout_session_id,
			user_email,
			plan,
			status,
			dry_run,
			started_at,
			finished_at,
			duration_seconds,
			total_count,
			processed_count,
			progress_percent,
			error,
			stats,
			created_at
		)
		VALUES ($1,$2,$3,$4,$5,to_timestamp(NULLIF($6,0)),to_timestamp(NULLIF($7,0)),$8,$9,$10,$11,$12,$13,to_timestamp($14))
		ON CONFLICT (checkout_session_id)
		DO UPDATE SET
			user_email = EXCLUDED.user_email,
			plan = EXCLUDED.plan,
			status = EXCLUDED.status,
			dry_run = EXCLUDED.dry_run,
			started_at = EXCLUDED.started_at,
			finished_at = EXCLUDED.finished_at,
			duration_seconds = EXCLUDED.duration_seconds,
			total_count = EXCLUDED.total_count,
			processed_count = EXCLUDED.processed_count,
			progress_percent = EXCLUDED.progress_percent,
			error = EXCLUDED.error,
			stats = EXCLUDED.stats
	`,
		record.CheckoutSessionID,
		record.UserEmail,
		record.Plan,
		record.Status,
		record.DryRun,
		record.StartedAtUnix,
		record.FinishedAtUnix,
		record.DurationSeconds,
		record.TotalCount,
		record.ProcessedCount,
		record.ProgressPercent,
		record.Error,
		statsJSON,
		record.CreatedAtUnix,
	)

	return err
}

func (s *PostgresStore) ListRunsByEmail(ctx context.Context, email string, limit int) ([]RunRecord, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	rows, err := s.pool.Query(ctx, `
		SELECT
			checkout_session_id,
			user_email,
			plan,
			status,
			dry_run,
			COALESCE(EXTRACT(EPOCH FROM started_at)::bigint, 0),
			COALESCE(EXTRACT(EPOCH FROM finished_at)::bigint, 0),
			duration_seconds,
			total_count,
			processed_count,
			progress_percent,
			error,
			stats,
			EXTRACT(EPOCH FROM created_at)::bigint
		FROM cleanup_history
		WHERE user_email = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, strings.TrimSpace(email), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	runs := make([]RunRecord, 0, limit)
	for rows.Next() {
		var rec RunRecord
		var statsRaw []byte
		if err := rows.Scan(
			&rec.CheckoutSessionID,
			&rec.UserEmail,
			&rec.Plan,
			&rec.Status,
			&rec.DryRun,
			&rec.StartedAtUnix,
			&rec.FinishedAtUnix,
			&rec.DurationSeconds,
			&rec.TotalCount,
			&rec.ProcessedCount,
			&rec.ProgressPercent,
			&rec.Error,
			&statsRaw,
			&rec.CreatedAtUnix,
		); err != nil {
			return nil, err
		}

		_ = json.Unmarshal(statsRaw, &rec.Stats)
		runs = append(runs, rec)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return runs, nil
}

func (s *PostgresStore) ensureSchema(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS cleanup_history (
			id BIGSERIAL PRIMARY KEY,
			checkout_session_id TEXT NOT NULL UNIQUE,
			user_email TEXT NOT NULL,
			plan TEXT NOT NULL,
			status TEXT NOT NULL,
			dry_run BOOLEAN NOT NULL,
			started_at TIMESTAMPTZ,
			finished_at TIMESTAMPTZ,
			duration_seconds INTEGER NOT NULL DEFAULT 0,
			total_count INTEGER NOT NULL DEFAULT 0,
			processed_count INTEGER NOT NULL DEFAULT 0,
			progress_percent INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			stats JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		CREATE INDEX IF NOT EXISTS idx_cleanup_history_user_created
		ON cleanup_history (user_email, created_at DESC);
	`)
	return err
}

func (s *PostgresStore) Close() {
	if s == nil || s.pool == nil {
		return
	}
	s.pool.Close()
}
