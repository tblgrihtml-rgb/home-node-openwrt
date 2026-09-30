package maintenance

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"homenode/services/home-api/internal/store"
)

func TestBackupIfDueCreatesFirstBackup(t *testing.T) {
	dir := t.TempDir()
	database, err := store.Open(filepath.Join(dir, "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	backupDir := filepath.Join(dir, "backups")
	backupIfDue(context.Background(), database, backupDir, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now())
	backups, err := listBackups(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("got %d backups, want 1", len(backups))
	}
	copyDB, err := store.Open(backups[0].path)
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	if result, err := copyDB.QuickCheck(context.Background()); err != nil || result != "ok" {
		t.Fatalf("backup quick_check = %q, %v", result, err)
	}
}
