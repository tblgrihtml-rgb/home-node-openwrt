package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"homenode/services/home-api/internal/config"
)

func TestMediaStreamSupportsByteRanges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "anime", "Title", "episode-01.mp4")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}

	server := &Server{cfg: config.Config{MediaRoot: root}}
	request := httptest.NewRequest(http.MethodGet, "/api/media/stream?path=anime%2FTitle%2Fepisode-01.mp4", nil)
	request.Header.Set("Range", "bytes=2-5")
	recorder := httptest.NewRecorder()
	server.mediaStream(recorder, request)

	if recorder.Code != http.StatusPartialContent || recorder.Body.String() != "2345" {
		t.Fatalf("unexpected range response: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("unexpected content type: %q", recorder.Header().Get("Content-Type"))
	}
}

func TestMediaStreamSupportsAudio(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "music", "Artist", "Album", "01-track.mp3")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}

	server := &Server{cfg: config.Config{MediaRoot: root}}
	request := httptest.NewRequest(http.MethodGet, "/api/media/stream?path=music%2FArtist%2FAlbum%2F01-track.mp3", nil)
	request.Header.Set("Range", "bytes=1-3")
	recorder := httptest.NewRecorder()
	server.mediaStream(recorder, request)

	if recorder.Code != http.StatusPartialContent || recorder.Body.String() != "123" {
		t.Fatalf("unexpected audio range response: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("unexpected audio content type: %q", recorder.Header().Get("Content-Type"))
	}
}

func TestOpenMediaFileRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside.mp4")
	if err := os.WriteFile(outside, []byte("private"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	file, _, err := openMediaFile(root, "../outside.mp4")
	if file != nil {
		file.Close()
	}
	if err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
}

func TestMediaCompatibleStreamsMP4FromFFmpeg(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "anime", "Title", "episode-01.mkv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nprintf 'fragmented-mp4'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	server := &Server{cfg: config.Config{MediaRoot: root, FFmpegPath: ffmpeg}}
	request := httptest.NewRequest(http.MethodGet, "/api/media/compatible?path=anime%2FTitle%2Fepisode-01.mkv", nil)
	recorder := httptest.NewRecorder()
	server.mediaCompatible(recorder, request)

	if recorder.Code != http.StatusOK || recorder.Body.String() != "fragmented-mp4" {
		t.Fatalf("unexpected compatible response: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("unexpected content type: %q", recorder.Header().Get("Content-Type"))
	}
}
