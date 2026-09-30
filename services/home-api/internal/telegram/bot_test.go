package telegram

import (
	"testing"

	"homenode/services/home-api/internal/store"
	"homenode/services/home-api/internal/transmission"
)

func TestSplitCommand(t *testing.T) {
	command, argument := splitCommand("/search@HomeNodeBot Провожающая Фрирен")
	if command != "/search" || argument != "Провожающая Фрирен" {
		t.Fatalf("unexpected command=%q argument=%q", command, argument)
	}
}

func TestShortPreservesUnicode(t *testing.T) {
	if got := short("Провожающая в последний путь Фрирен", 12); got != "Провожающая…" {
		t.Fatalf("unexpected short title: %q", got)
	}
}

func TestClassifyCompletedDownloadStopsWithoutTrackerError(t *testing.T) {
	previous := store.TelegramDownloadState{PercentDone: 0.9}
	item := transmission.Torrent{ID: 7, PercentDone: 1, Status: 6, ErrorString: "Tracker HTTP response 404 (Not Found)"}
	completed, failed, shouldStop := classifyDownload(previous, true, item)
	if !completed || failed || !shouldStop {
		t.Fatalf("unexpected actions: completed=%v failed=%v stop=%v", completed, failed, shouldStop)
	}
}

func TestClassifyIncompleteDownloadError(t *testing.T) {
	previous := store.TelegramDownloadState{PercentDone: 0.4}
	item := transmission.Torrent{ID: 8, PercentDone: 0.5, Status: 4, ErrorString: "disk error"}
	completed, failed, shouldStop := classifyDownload(previous, true, item)
	if completed || !failed || shouldStop {
		t.Fatalf("unexpected actions: completed=%v failed=%v stop=%v", completed, failed, shouldStop)
	}
}
