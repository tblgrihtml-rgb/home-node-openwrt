package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

type Event struct {
	ID        int64  `json:"id"`
	Source    string `json:"source"`
	Action    string `json:"action"`
	ItemID    string `json:"item_id"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
}

type MediaTitle struct {
	Folder    string `json:"folder"`
	Title     string `json:"title"`
	PosterURL string `json:"poster_url"`
	Source    string `json:"source"`
	ReleaseID int64  `json:"release_id"`
}

type TelegramOwner struct {
	UserID   int64  `json:"user_id"`
	ChatID   int64  `json:"chat_id"`
	Username string `json:"username"`
	PairedAt string `json:"paired_at"`
}

type TelegramDownloadState struct {
	PercentDone float64
	ErrorString string
}

type WatchProgress struct {
	Path            string  `json:"path"`
	Title           string  `json:"title"`
	Category        string  `json:"category"`
	PositionSeconds float64 `json:"position_seconds"`
	DurationSeconds float64 `json:"duration_seconds"`
	Completed       bool    `json:"completed"`
	UpdatedAt       string  `json:"updated_at"`
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA busy_timeout=5000;
		CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			source TEXT NOT NULL,
			action TEXT NOT NULL,
			item_id TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_events_created_at ON events(created_at DESC);
		CREATE TABLE IF NOT EXISTS media_titles (
			folder TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			poster_url TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			release_id INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS telegram_owner (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			user_id INTEGER NOT NULL,
			chat_id INTEGER NOT NULL,
			username TEXT NOT NULL DEFAULT '',
			paired_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS telegram_state (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS telegram_download_state (
			torrent_id INTEGER PRIMARY KEY,
			percent_done REAL NOT NULL DEFAULT 0,
			error_string TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS watch_progress (
			path TEXT PRIMARY KEY,
			title TEXT NOT NULL DEFAULT '',
			category TEXT NOT NULL DEFAULT '',
			position_seconds REAL NOT NULL DEFAULT 0,
			duration_seconds REAL NOT NULL DEFAULT 0,
			completed INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_watch_progress_updated_at ON watch_progress(updated_at DESC);
	`); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) TelegramOwner(ctx context.Context) (TelegramOwner, bool, error) {
	var owner TelegramOwner
	err := s.db.QueryRowContext(ctx, `SELECT user_id, chat_id, username, paired_at FROM telegram_owner WHERE id = 1`).
		Scan(&owner.UserID, &owner.ChatID, &owner.Username, &owner.PairedAt)
	if err == sql.ErrNoRows {
		return TelegramOwner{}, false, nil
	}
	return owner, err == nil, err
}

func (s *Store) SetTelegramOwner(ctx context.Context, owner TelegramOwner) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO telegram_owner(id, user_id, chat_id, username, paired_at)
		VALUES(1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			user_id=excluded.user_id,
			chat_id=excluded.chat_id,
			username=excluded.username,
			paired_at=excluded.paired_at`,
		owner.UserID, owner.ChatID, owner.Username, owner.PairedAt)
	return err
}

func (s *Store) ClearTelegramOwner(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM telegram_owner WHERE id = 1`)
	return err
}

func (s *Store) TelegramOffset(ctx context.Context) (int64, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM telegram_state WHERE key = 'update_offset'`).Scan(&value)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(value, 10, 64)
}

func (s *Store) SetTelegramOffset(ctx context.Context, offset int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO telegram_state(key, value) VALUES('update_offset', ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.FormatInt(offset, 10))
	return err
}

func (s *Store) TelegramDownloadStates(ctx context.Context) (map[int64]TelegramDownloadState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT torrent_id, percent_done, error_string FROM telegram_download_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := make(map[int64]TelegramDownloadState)
	for rows.Next() {
		var id int64
		var state TelegramDownloadState
		if err := rows.Scan(&id, &state.PercentDone, &state.ErrorString); err != nil {
			return nil, err
		}
		states[id] = state
	}
	return states, rows.Err()
}

func (s *Store) SetTelegramDownloadState(ctx context.Context, torrentID int64, state TelegramDownloadState) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO telegram_download_state(torrent_id, percent_done, error_string, updated_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(torrent_id) DO UPDATE SET
			percent_done=excluded.percent_done,
			error_string=excluded.error_string,
			updated_at=excluded.updated_at`,
		torrentID, state.PercentDone, state.ErrorString, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) MediaTitle(ctx context.Context, folder string) (MediaTitle, bool, error) {
	var item MediaTitle
	err := s.db.QueryRowContext(ctx, `SELECT folder, title, poster_url, source, release_id FROM media_titles WHERE folder = ?`, folder).
		Scan(&item.Folder, &item.Title, &item.PosterURL, &item.Source, &item.ReleaseID)
	if err == sql.ErrNoRows {
		return MediaTitle{}, false, nil
	}
	return item, err == nil, err
}

func (s *Store) UpsertMediaTitle(ctx context.Context, item MediaTitle) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO media_titles(folder, title, poster_url, source, release_id, updated_at)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(folder) DO UPDATE SET
			title=excluded.title,
			poster_url=excluded.poster_url,
			source=excluded.source,
			release_id=excluded.release_id,
			updated_at=excluded.updated_at`,
		item.Folder, item.Title, item.PosterURL, item.Source, item.ReleaseID, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) SetWatchProgress(ctx context.Context, progress WatchProgress) error {
	updatedAt := progress.UpdatedAt
	if updatedAt == "" {
		updatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO watch_progress(path, title, category, position_seconds, duration_seconds, completed, updated_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET
			title=excluded.title,
			category=excluded.category,
			position_seconds=excluded.position_seconds,
			duration_seconds=excluded.duration_seconds,
			completed=excluded.completed,
			updated_at=excluded.updated_at`,
		progress.Path, progress.Title, progress.Category, progress.PositionSeconds, progress.DurationSeconds, progress.Completed, updatedAt)
	return err
}

func (s *Store) RecentWatchProgress(ctx context.Context, limit int) ([]WatchProgress, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT path, title, category, position_seconds, duration_seconds, completed, updated_at
		FROM watch_progress
		ORDER BY updated_at DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]WatchProgress, 0, limit)
	for rows.Next() {
		var item WatchProgress
		if err := rows.Scan(&item.Path, &item.Title, &item.Category, &item.PositionSeconds, &item.DurationSeconds, &item.Completed, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) Close() error { return s.db.Close() }

// Backup creates a consistent SQLite copy while the application remains online.
func (s *Store) Backup(ctx context.Context, destination string) error {
	quoted := strings.ReplaceAll(destination, "'", "''")
	_, err := s.db.ExecContext(ctx, fmt.Sprintf("VACUUM INTO '%s'", quoted))
	return err
}

func (s *Store) QuickCheck(ctx context.Context) (string, error) {
	var result string
	err := s.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result)
	return result, err
}

func (s *Store) Log(ctx context.Context, source, action, itemID, title string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO events(source, action, item_id, title, created_at) VALUES(?,?,?,?,?)`,
		source, action, itemID, title, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) Recent(ctx context.Context, limit int) ([]Event, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, source, action, item_id, title, created_at FROM events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]Event, 0, limit)
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.ID, &event.Source, &event.Action, &event.ItemID, &event.Title, &event.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
