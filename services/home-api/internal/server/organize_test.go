package server

import (
	"testing"

	"homenode/services/home-api/internal/transmission"
)

func TestSafeFolderName(t *testing.T) {
	got := safeFolderName(`  Провожающая: Фрирен / сезон 1  `)
	if got != "Провожающая Фрирен сезон 1" {
		t.Fatalf("unexpected folder name: %q", got)
	}
}

func TestCommonTorrentRoot(t *testing.T) {
	files := []transmission.TorrentFile{{Name: "Release/01.mkv"}, {Name: "Release/02.mkv"}}
	root, ok := commonTorrentRoot(files)
	if !ok || root != "Release" {
		t.Fatalf("unexpected root: %q, %v", root, ok)
	}
	if _, ok := commonTorrentRoot([]transmission.TorrentFile{{Name: "01.mkv"}}); ok {
		t.Fatal("single file torrent must use a dedicated destination folder")
	}
}

func TestAddedTorrentID(t *testing.T) {
	id, ok := addedTorrentID(map[string]any{"torrent-added": map[string]any{"id": float64(42)}})
	if !ok || id != 42 {
		t.Fatalf("unexpected id: %d, %v", id, ok)
	}
}
