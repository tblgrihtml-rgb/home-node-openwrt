package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type MusicJob struct {
	ID            int64  `json:"id"`
	AppleTrackID  int64  `json:"apple_track_id"`
	Title         string `json:"title"`
	Artist        string `json:"artist"`
	Album         string `json:"album"`
	Status        string `json:"status"`
	Attempts      int    `json:"attempts"`
	NextAttemptAt string `json:"next_attempt_at,omitempty"`
	Error         string `json:"error,omitempty"`
	RelativePath  string `json:"relative_path,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

const musicJobColumns = `id, apple_track_id, title, artist, album, status, attempts,
	next_attempt_at, error, relative_path, created_at, updated_at`

func initMusicJobs(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS music_jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			apple_track_id INTEGER NOT NULL UNIQUE,
			title TEXT NOT NULL,
			artist TEXT NOT NULL,
			album TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'queued',
			attempts INTEGER NOT NULL DEFAULT 0,
			next_attempt_at TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			relative_path TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_music_jobs_ready ON music_jobs(status, next_attempt_at, id);
		CREATE TABLE IF NOT EXISTS music_provider_cooldown (
			provider TEXT PRIMARY KEY,
			until TEXT NOT NULL
		);
		UPDATE music_jobs SET status='queued', updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE status='running';
	`)
	return err
}

func scanMusicJob(row interface{ Scan(...any) error }) (MusicJob, error) {
	var job MusicJob
	err := row.Scan(&job.ID, &job.AppleTrackID, &job.Title, &job.Artist, &job.Album,
		&job.Status, &job.Attempts, &job.NextAttemptAt, &job.Error, &job.RelativePath,
		&job.CreatedAt, &job.UpdatedAt)
	return job, err
}

func (s *Store) AddMusicJob(ctx context.Context, appleTrackID int64, title, artist, album string) (MusicJob, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `INSERT INTO music_jobs
		(apple_track_id,title,artist,album,status,created_at,updated_at)
		VALUES(?,?,?,?, 'queued',?,?) ON CONFLICT(apple_track_id) DO NOTHING`,
		appleTrackID, title, artist, album, now, now)
	if err != nil {
		return MusicJob{}, err
	}
	return scanMusicJob(s.db.QueryRowContext(ctx, `SELECT `+musicJobColumns+` FROM music_jobs WHERE apple_track_id=?`, appleTrackID))
}

func (s *Store) ListMusicJobs(ctx context.Context) ([]MusicJob, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+musicJobColumns+` FROM music_jobs ORDER BY id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]MusicJob, 0)
	for rows.Next() {
		job, err := scanMusicJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) ClaimMusicJob(ctx context.Context) (MusicJob, bool, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	job, err := scanMusicJob(s.db.QueryRowContext(ctx, `UPDATE music_jobs SET
		status='running', attempts=attempts+1, error='', updated_at=?
		WHERE id=(SELECT id FROM music_jobs WHERE status IN ('queued','retry')
			AND (next_attempt_at='' OR next_attempt_at<=?) ORDER BY id LIMIT 1)
		RETURNING `+musicJobColumns, now, now))
	if errors.Is(err, sql.ErrNoRows) {
		return MusicJob{}, false, nil
	}
	return job, err == nil, err
}

func (s *Store) UpdateMusicJob(ctx context.Context, id int64, status, message, nextAttemptAt, relativePath string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE music_jobs SET status=?, error=?, next_attempt_at=?, relative_path=?, updated_at=?
		WHERE id=? AND status='running'`, status, message, nextAttemptAt, relativePath,
		time.Now().UTC().Format(time.RFC3339), id)
	return err
}

func (s *Store) ResumeMusicVerificationJobs(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE music_jobs SET status='queued', error='', next_attempt_at='', updated_at=?
		WHERE status='verification'`, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) RetryMusicJob(ctx context.Context, id int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE music_jobs SET status='queued', attempts=0, error='', next_attempt_at='', updated_at=?
		WHERE id=? AND status IN ('failed','verification','retry')`, time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (s *Store) RemoveMusicJob(ctx context.Context, id int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM music_jobs WHERE id=? AND status IN ('queued','retry','verification','failed','done')`, id)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (s *Store) MusicCooldown(ctx context.Context) (time.Time, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT until FROM music_provider_cooldown WHERE provider='spotiflac-tidal'`).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, value)
}

func (s *Store) SetMusicCooldown(ctx context.Context, until time.Time) error {
	value := until.UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `INSERT INTO music_provider_cooldown(provider,until) VALUES('spotiflac-tidal',?)
		ON CONFLICT(provider) DO UPDATE SET until=MAX(until,excluded.until)`, value)
	return err
}
