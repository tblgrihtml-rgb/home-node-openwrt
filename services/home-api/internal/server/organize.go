package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"homenode/services/home-api/internal/transmission"
)

func (s *Server) organizeTorrent(result map[string]any, title, libraryRoot string) {
	id, ok := addedTorrentID(result)
	if !ok {
		return
	}
	folder := safeFolderName(title)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			layout, err := s.transmission.Layout(ctx, id)
			if err == nil && layout.MetadataPercentComplete >= 1 && len(layout.Files) > 0 {
				if err := s.applyTorrentLayout(ctx, id, folder, libraryRoot, layout.Files); err != nil {
					s.logger.Error("organize torrent", "id", id, "title", folder, "error", err)
					return
				}
				_ = s.store.Log(context.Background(), "transmission", "organize", formatID(id), folder)
				return
			}
			select {
			case <-ctx.Done():
				s.logger.Error("organize torrent timeout", "id", id, "error", ctx.Err())
				return
			case <-ticker.C:
			}
		}
	}()
}

func (s *Server) applyTorrentLayout(ctx context.Context, id int64, folder, libraryRoot string, files []transmission.TorrentFile) error {
	if root, ok := commonTorrentRoot(files); ok {
		if root == folder {
			return nil
		}
		return s.transmission.RenamePath(ctx, id, root, folder)
	}
	target := filepath.Join(libraryRoot, folder)
	if filepath.Dir(target) != filepath.Clean(libraryRoot) {
		return errors.New("unsafe library path")
	}
	if err := os.MkdirAll(target, 0o775); err != nil {
		return err
	}
	if err := os.Chmod(target, os.ModeSetgid|0o775); err != nil {
		return err
	}
	return s.transmission.SetLocation(ctx, id, target, true)
}

func commonTorrentRoot(files []transmission.TorrentFile) (string, bool) {
	if len(files) < 2 {
		return "", false
	}
	root := ""
	for _, file := range files {
		parts := strings.SplitN(filepath.ToSlash(file.Name), "/", 2)
		if len(parts) != 2 || parts[0] == "" {
			return "", false
		}
		if root == "" {
			root = parts[0]
		} else if root != parts[0] {
			return "", false
		}
	}
	return root, root != ""
}

func safeFolderName(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`<>:"/\\|?*`, r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	value = strings.Trim(value, " .")
	if value == "" {
		return "Без названия"
	}
	runes := []rune(value)
	if len(runes) > 120 {
		value = strings.TrimSpace(string(runes[:120]))
	}
	return value
}

func addedTorrentID(result map[string]any) (int64, bool) {
	for _, key := range []string{"torrent-added", "torrent-duplicate"} {
		item, ok := result[key].(map[string]any)
		if !ok {
			continue
		}
		switch id := item["id"].(type) {
		case float64:
			return int64(id), id > 0
		case int64:
			return id, id > 0
		}
	}
	return 0, false
}

func formatID(id int64) string {
	return strconv.FormatInt(id, 10)
}
