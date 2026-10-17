package telegram

import (
	"testing"

	"homenode/services/home-api/internal/aniliberty"
	"homenode/services/home-api/internal/store"
	"homenode/services/home-api/internal/transmission"
)

func TestLatestEpisodeUsesHighestOrdinal(t *testing.T) {
	episodes := []aniliberty.Episode{
		{ID: "e2", Ordinal: 2, Name: "Вторая"},
		{ID: "e1", Ordinal: 1, Name: "Первая"},
		{ID: "e25", Ordinal: 2.5, Name: "Спецвыпуск"},
	}
	got, ok := latestEpisode(episodes)
	if !ok || got.ID != "e25" || got.Ordinal != 2.5 {
		t.Fatalf("latest episode = %#v, ok=%v", got, ok)
	}
	if _, ok := latestEpisode(nil); ok {
		t.Fatal("empty episode list should have no latest episode")
	}
}

func TestSelectAnimeTorrentPrefers1080pAndActiveSeeders(t *testing.T) {
	torrents := []aniliberty.Torrent{
		{ID: 1, Label: "Series [1-3]", Magnet: "magnet:?xt=urn:btih:one", Seeders: 200, Quality: struct {
			Value       string `json:"value"`
			Description string `json:"description"`
		}{Value: "720p", Description: "720p"}},
		{ID: 2, Label: "Series [1-2]", Magnet: "magnet:?xt=urn:btih:two", Seeders: 90, Quality: struct {
			Value       string `json:"value"`
			Description string `json:"description"`
		}{Value: "1080p", Description: "1080p"}},
		{ID: 3, Label: "Series [1-3]", Magnet: "magnet:?xt=urn:btih:three", Seeders: 50, Size: 100, Quality: struct {
			Value       string `json:"value"`
			Description string `json:"description"`
		}{Value: "1080p", Description: "1080p"}},
		{ID: 4, Label: "Series [1-3]", Magnet: "", Seeders: 999, Quality: struct {
			Value       string `json:"value"`
			Description string `json:"description"`
		}{Value: "1080p", Description: "1080p"}},
	}
	got, ok := selectAnimeTorrent(torrents, 3)
	if !ok || got.ID != 3 {
		t.Fatalf("selected torrent = %#v, ok=%v", got, ok)
	}
	if _, ok := selectAnimeTorrent(torrents, 4); ok {
		t.Fatal("should not choose a torrent that does not contain the new episode")
	}
}

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

func TestClassifyIncompleteDownloadLocalErrorOnce(t *testing.T) {
	previous := store.TelegramDownloadState{PercentDone: 0.4}
	item := transmission.Torrent{ID: 8, PercentDone: 0.5, Status: 4, Error: 3, ErrorString: "disk error"}
	completed, failed, shouldStop := classifyDownload(previous, true, item)
	if completed || !failed || shouldStop {
		t.Fatalf("unexpected actions: completed=%v failed=%v stop=%v", completed, failed, shouldStop)
	}
	previous.Error = 3
	item.ErrorString = "permission denied"
	_, failed, _ = classifyDownload(previous, true, item)
	if failed {
		t.Fatal("changing local-error text must not trigger repeated notification")
	}
}

func TestClassifyTrackerErrorsWithoutNotification(t *testing.T) {
	previous := store.TelegramDownloadState{PercentDone: 0.4, Error: 2, ErrorString: "tracker timeout"}
	item := transmission.Torrent{ID: 8, PercentDone: 0.5, Status: 4, Error: 2, ErrorString: "tracker HTTP 503"}
	_, failed, _ := classifyDownload(previous, true, item)
	if failed {
		t.Fatal("tracker errors must not be reported as local download failures")
	}
}
