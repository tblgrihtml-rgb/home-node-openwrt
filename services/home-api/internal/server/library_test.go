package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"homenode/services/home-api/internal/config"
)

func TestLibraryGroupsMusicByArtistAndAlbum(t *testing.T) {
	root := t.TempDir()
	album := filepath.Join(root, "music", "Artist", "Album")
	if err := os.MkdirAll(album, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"01-first.mp3", "02-second.flac"} {
		if err := os.WriteFile(filepath.Join(album, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	server := &Server{cfg: config.Config{MediaRoot: root}}
	recorder := httptest.NewRecorder()
	server.library(recorder, httptest.NewRequest(http.MethodGet, "/api/library", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", recorder.Code)
	}
	var items []LibraryTitle
	if err := json.Unmarshal(recorder.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one album, got %d", len(items))
	}
	item := items[0]
	if item.Category != "music" || item.Artist != "Artist" || item.Title != "Album" || item.FileCount != 2 {
		t.Fatalf("unexpected album: %+v", item)
	}
	if item.SMBPath != "smb://192.168.1.1/HomeNode/media/music/Artist/Album" {
		t.Fatalf("unexpected SMB path: %q", item.SMBPath)
	}
}
