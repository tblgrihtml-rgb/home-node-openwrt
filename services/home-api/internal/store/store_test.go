package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestTelegramState(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	if _, ok, err := database.TelegramOwner(ctx); err != nil || ok {
		t.Fatalf("unexpected initial owner: ok=%v err=%v", ok, err)
	}
	owner := TelegramOwner{UserID: 10, ChatID: 20, Username: "owner", PairedAt: "2026-09-26T00:00:00Z"}
	if err := database.SetTelegramOwner(ctx, owner); err != nil {
		t.Fatal(err)
	}
	got, ok, err := database.TelegramOwner(ctx)
	if err != nil || !ok || got != owner {
		t.Fatalf("owner mismatch: got=%#v ok=%v err=%v", got, ok, err)
	}
	if err := database.SetTelegramOffset(ctx, 123); err != nil {
		t.Fatal(err)
	}
	if offset, err := database.TelegramOffset(ctx); err != nil || offset != 123 {
		t.Fatalf("offset=%d err=%v", offset, err)
	}
	state := TelegramDownloadState{PercentDone: 0.75, ErrorString: ""}
	if err := database.SetTelegramDownloadState(ctx, 42, state); err != nil {
		t.Fatal(err)
	}
	states, err := database.TelegramDownloadStates(ctx)
	if err != nil || states[42] != state {
		t.Fatalf("download states=%#v err=%v", states, err)
	}
	if err := database.ClearTelegramOwner(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := database.TelegramOwner(ctx); err != nil || ok {
		t.Fatalf("owner was not cleared: ok=%v err=%v", ok, err)
	}
}

func TestWatchProgressUpsertAndRecentOrder(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	older := WatchProgress{
		Path: "anime/Title/01.mkv", Title: "Title", Category: "anime",
		PositionSeconds: 120, DurationSeconds: 1440, UpdatedAt: "2026-09-28T10:00:00Z",
	}
	newer := WatchProgress{
		Path: "movies/Movie/movie.mkv", Title: "Movie", Category: "movies",
		PositionSeconds: 240, DurationSeconds: 7200, UpdatedAt: "2026-09-28T11:00:00Z",
	}
	for _, item := range []WatchProgress{older, newer} {
		if err := database.SetWatchProgress(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	older.PositionSeconds = 300
	older.Completed = true
	older.UpdatedAt = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	if err := database.SetWatchProgress(ctx, older); err != nil {
		t.Fatal(err)
	}

	items, err := database.RecentWatchProgress(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Path != older.Path || items[0].PositionSeconds != 300 || !items[0].Completed {
		t.Fatalf("unexpected watch progress: %#v", items)
	}
}
