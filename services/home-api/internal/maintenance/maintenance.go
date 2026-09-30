package maintenance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"homenode/services/home-api/internal/store"
)

const (
	backupInterval = 24 * time.Hour
	backupKeep     = 7
)

func Run(ctx context.Context, database *store.Store, backupDir string, logger *slog.Logger) {
	backupIfDue(ctx, database, backupDir, logger, time.Now())
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			backupIfDue(ctx, database, backupDir, logger, now)
		}
	}
}

func backupIfDue(ctx context.Context, database *store.Store, backupDir string, logger *slog.Logger, now time.Time) {
	if err := os.MkdirAll(backupDir, 0750); err != nil {
		logger.Error("create backup directory", "error", err)
		return
	}
	backups, err := listBackups(backupDir)
	if err != nil {
		logger.Error("list backups", "error", err)
		return
	}
	if len(backups) > 0 && now.Sub(backups[0].modTime) < backupInterval {
		return
	}
	finalPath := filepath.Join(backupDir, fmt.Sprintf("homenode-%s.db", now.Format("20060102-150405")))
	temporaryPath := finalPath + ".tmp"
	_ = os.Remove(temporaryPath)
	if err := database.Backup(ctx, temporaryPath); err != nil {
		logger.Error("database backup", "error", err)
		_ = os.Remove(temporaryPath)
		return
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		logger.Error("publish database backup", "error", err)
		_ = os.Remove(temporaryPath)
		return
	}
	logger.Info("database backup created", "path", finalPath)
	backups, _ = listBackups(backupDir)
	if len(backups) > backupKeep {
		for _, old := range backups[backupKeep:] {
			if err := os.Remove(old.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				logger.Warn("remove old database backup", "path", old.path, "error", err)
			}
		}
	}
}

type backupFile struct {
	path    string
	modTime time.Time
}

func listBackups(dir string) ([]backupFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	result := make([]backupFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "homenode-") || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		result = append(result, backupFile{path: filepath.Join(dir, entry.Name()), modTime: info.ModTime()})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].modTime.After(result[j].modTime) })
	return result, nil
}
