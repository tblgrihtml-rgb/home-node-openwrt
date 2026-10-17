package music

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"homenode/services/home-api/internal/store"
)

func TestSafeName(t *testing.T) {
	if got := safeName("../A/B\\C: Track?\n"); got != "A B C Track" {
		t.Fatalf("unsafe name: %q", got)
	}
	if got := safeName("..."); got != "Без названия" {
		t.Fatalf("empty name: %q", got)
	}
}

func TestPublicMediaURL(t *testing.T) {
	for _, raw := range []string{"http://example.com/a.flac", "https://127.0.0.1/a.flac", "https://192.168.1.1/a.flac", "https://user:pass@example.com/a.flac"} {
		if _, err := publicMediaURL(raw); err == nil {
			t.Errorf("accepted unsafe URL %q", raw)
		}
	}
	if _, err := publicMediaURL("https://example.com/a.flac"); err != nil {
		t.Fatal(err)
	}
}

func TestRetryAfter(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("Retry-After", "120")
	if got := retryDelay(resp); got != 2*time.Minute {
		t.Fatalf("retry delay: %v", got)
	}
	resp.Header.Del("Retry-After")
	if got := retryDelay(resp); got != 30*time.Minute {
		t.Fatalf("fallback delay: %v", got)
	}
}

func TestVerificationURLRejectsOtherDestinations(t *testing.T) {
	client, err := New(t.TempDir() + "/session.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"", "javascript:alert(1)", "https://user:pass@example.com",
		"http://192.168.1.1:53672/session-grant?state=x&grant=y",
		"http://127.0.0.1:53672/other?state=x&grant=y",
		"http://127.0.0.1:53672/session-grant?state=x",
		"http://127.0.0.1:53672/session-grant?state=x&state=z&grant=y",
	} {
		if err := client.CompleteVerificationURL(context.Background(), raw); err == nil {
			t.Errorf("accepted unsafe callback %q", raw)
		}
	}
}

func TestDownloadRefusesUnMountedDirectory(t *testing.T) {
	client := &Client{}
	_, err := client.Download(context.Background(), store.MusicJob{AppleTrackID: 1}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "SSD") {
		t.Fatalf("expected missing SSD error, got %v", err)
	}
}
