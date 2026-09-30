package telegram

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"homenode/services/home-api/internal/store"
)

func TestSafeMusicPart(t *testing.T) {
	if got := safeMusicPart("../Певец/Альбом:*", "unknown"); strings.ContainsAny(got, "/\\:*") || got == ".." {
		t.Fatalf("unsafe path part: %q", got)
	}
	if got := safeMusicPart("..", "unknown"); got != "unknown" {
		t.Fatalf("expected fallback, got %q", got)
	}
}

func TestImportMusicAndDuplicate(t *testing.T) {
	root := t.TempDir()
	database, err := store.Open(filepath.Join(root, "music.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	content := []byte("test audio content")
	var downloads int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"file_path": "music/track.mp3", "file_size": len(content)}})
		case strings.HasPrefix(r.URL.Path, "/file/bot"):
			downloads++
			_, _ = w.Write(content)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
		}
	}))
	defer server.Close()
	bot := New(Config{Token: "test-token", APIBaseURL: server.URL, MusicDir: filepath.Join(root, "music")}, database, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	message := &Message{Chat: Chat{ID: 1, Type: "private"}, Audio: &AudioFile{
		FileID: "file-1", FileUniqueID: "unique-1", FileName: "Song.mp3", FileSize: int64(len(content)), Performer: "Artist", Title: "Song",
	}}
	for i := 0; i < 2; i++ {
		if err := bot.importMusic(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}
	if downloads != 1 {
		t.Fatalf("expected one download, got %d", downloads)
	}
	items, err := database.TelegramMusicTracks(context.Background(), 10, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("tracks=%v err=%v", items, err)
	}
	if items[0].Artist != "Artist" || items[0].Album != "Без альбома" {
		t.Fatalf("unexpected metadata: %#v", items[0])
	}
	path, err := bot.musicPath(items[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil || string(stored) != string(content) {
		t.Fatalf("stored file mismatch: %q, %v", stored, err)
	}
}

func TestMusicPathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	bot := &Bot{cfg: Config{MusicDir: root}}
	outside := filepath.Join(filepath.Dir(root), "outside.mp3")
	if _, err := bot.musicPath("../" + filepath.Base(outside)); err == nil {
		t.Fatal("traversal was accepted")
	}
}

func TestLocalBotAPILinksLargeFileFromRouterSSD(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "cache")
	if err := os.MkdirAll(filepath.Join(cache, "documents"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(cache, "documents", "track.mp3")
	const size = 21 << 20
	file, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(size); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/getFile") {
			t.Fatalf("unexpected Telegram method: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{
			"file_path": "/Volumes/HomeNode/bot-cache/documents/track.mp3", "file_size": size,
		}})
	}))
	defer server.Close()
	client := newAPIClient(server.URL, "token", "192.0.2.1", "/Volumes/HomeNode/bot-cache", cache)
	target := filepath.Join(root, "import.mp3")
	n, err := client.downloadFile(context.Background(), "file-id", target)
	if err != nil || n != size {
		t.Fatalf("link large file: n=%d err=%v", n, err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Size() != size {
		t.Fatalf("unexpected target size: %#v err=%v", info, err)
	}
	if _, err := client.linkLocalFile(TelegramFile{FilePath: "/Volumes/HomeNode/other/track.mp3", FileSize: size}, filepath.Join(root, "bad.mp3")); err == nil {
		t.Fatal("outside Mac cache path was accepted")
	}
}
