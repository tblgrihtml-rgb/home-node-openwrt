package rutracker

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/charmap"
)

func TestParseSearch(t *testing.T) {
	fixture := `
	<table id="tor-tbl"><tbody>
	<tr><td><a class="f-name">Кино</a></td><td><a class="tLink" href="viewtopic.php?t=12345">Тестовый фильм [1080p]</a></td><td class="tor-size">12.4 GB</td><td class="seedmed">42</td><td class="leechmed">3</td></tr>
	<tr><td><a class="f-name">Аниме</a></td><td><a class="tLink" href="viewtopic.php?t=777">Тестовое аниме</a></td><td class="tor-size">2 GB</td><td class="seedmed">9</td><td class="leechmed">1</td></tr>
	</tbody></table>`
	results, err := ParseSearch(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].TopicID != 12345 || results[0].Seeders != 42 || results[0].Title != "Тестовый фильм [1080p]" {
		t.Fatalf("unexpected first result: %#v", results[0])
	}
}

func TestParseSearchAfterWindows1251Decoding(t *testing.T) {
	fixture := `<table id="tor-tbl"><tbody><tr><td><a class="f-name">Кино</a></td><td><a class="tLink" href="viewtopic.php?t=42">Матрица</a></td><td class="tor-size">1 GB</td><td class="seedmed">7</td><td class="leechmed">1</td></tr></tbody></table>`
	encoded, err := charmap.Windows1251.NewEncoder().Bytes([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	results, err := ParseSearch(charmap.Windows1251.NewDecoder().Reader(bytes.NewReader(encoded)))
	if err != nil || len(results) != 1 || results[0].Title != "Матрица" || results[0].Forum != "Кино" {
		t.Fatalf("unexpected decoded results: %#v, err=%v", results, err)
	}
}

func TestNotConfigured(t *testing.T) {
	client := New("https://example.invalid", "", "", "", 0)
	if client.Configured() {
		t.Fatal("client must not be configured")
	}
}

func TestProxyIsScopedToRuTrackerClient(t *testing.T) {
	client := New("https://example.invalid", "https://proxy.example:443", "user", "pass", 0)
	transport, ok := client.http.Transport.(*http.Transport)
	if !ok || transport.Proxy == nil {
		t.Fatal("expected a dedicated proxy transport")
	}
	request, _ := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
	proxy, err := transport.Proxy(request)
	if err != nil || proxy.String() != "https://proxy.example:443" {
		t.Fatalf("unexpected proxy: %v, err=%v", proxy, err)
	}
}

func TestResponseErrorExplainsCloudflareBlock(t *testing.T) {
	err := responseError("вход", http.StatusForbidden, []byte("<title>Just a moment...</title>"))
	if err == nil || !strings.Contains(err.Error(), "Cloudflare") || !strings.Contains(err.Error(), "служебный прокси") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDoRetriesTransientProxyFailure(t *testing.T) {
	client := New("https://example.invalid", "", "user", "pass", time.Second)
	attempts := 0
	client.http.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("temporary proxy failure")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	request, _ := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
	response, err := client.do(request)
	if err != nil || response.StatusCode != http.StatusOK || attempts != 2 {
		t.Fatalf("unexpected retry result: attempts=%d response=%v err=%v", attempts, response, err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
