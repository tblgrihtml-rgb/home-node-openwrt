package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"homenode/services/home-api/internal/config"
	"homenode/services/home-api/internal/store"
)

func TestWatchProgressUpdateAndList(t *testing.T) {
	root := t.TempDir()
	mediaPath := filepath.Join(root, "anime", "Title", "01.mkv")
	if err := os.MkdirAll(filepath.Dir(mediaPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mediaPath, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := &Server{cfg: config.Config{MediaRoot: root}, store: database}

	request := httptest.NewRequest(http.MethodPut, "/api/watch-progress", bytes.NewBufferString(`{
		"path":"anime/Title/01.mkv","title":"Title","position_seconds":95,"duration_seconds":100
	}`))
	response := httptest.NewRecorder()
	s.watchProgressUpdate(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("update failed: %d %s", response.Code, response.Body.String())
	}

	items, err := database.RecentWatchProgress(context.Background(), 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("unexpected items: %#v err=%v", items, err)
	}
	if !items[0].Completed || items[0].Category != "anime" {
		t.Fatalf("unexpected stored progress: %#v", items[0])
	}

	response = httptest.NewRecorder()
	s.watchProgressList(response, httptest.NewRequest(http.MethodGet, "/api/watch-progress?limit=10", nil))
	var listed []store.WatchProgress
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil || len(listed) != 1 {
		t.Fatalf("unexpected list response: %s err=%v", response.Body.String(), err)
	}
}

func TestWatchProgressRejectsPathOutsideMediaRoot(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := &Server{cfg: config.Config{MediaRoot: t.TempDir()}, store: database}
	request := httptest.NewRequest(http.MethodPut, "/api/watch-progress", bytes.NewBufferString(`{
		"path":"../../etc/passwd","title":"Nope","position_seconds":1,"duration_seconds":2
	}`))
	response := httptest.NewRecorder()
	s.watchProgressUpdate(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request, got %d: %s", response.Code, response.Body.String())
	}
}
