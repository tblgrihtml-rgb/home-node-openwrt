package aniliberty

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSearchAndTorrents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/search/releases":
			if got := r.URL.Query().Get("query"); got != "Перекур" {
				t.Fatalf("unexpected query: %q", got)
			}
			fmt.Fprint(w, `[{"id":10280,"year":2026,"alias":"smoke","name":{"main":"История о перекуре"}}]`)
		case "/anime/torrents/release/10280":
			fmt.Fprint(w, `[{"id":39029,"label":"1080p HEVC","magnet":"magnet:?xt=urn:btih:test","size":1330052689}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(server.URL, 2*time.Second)
	releases, err := client.Search(context.Background(), "Перекур")
	if err != nil || len(releases) != 1 || releases[0].ID != 10280 {
		t.Fatalf("unexpected search result: %#v, %v", releases, err)
	}
	torrents, err := client.Torrents(context.Background(), releases[0].ID)
	if err != nil || len(torrents) != 1 || torrents[0].ID != 39029 {
		t.Fatalf("unexpected torrents: %#v, %v", torrents, err)
	}
}

func TestSearchRejectsShortQuery(t *testing.T) {
	client := New("http://example.invalid", time.Second)
	if _, err := client.Search(context.Background(), "x"); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestCatalogSendsFilters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/anime/catalog/releases" {
			http.NotFound(w, r)
			return
		}
		checks := map[string]string{
			"f[search]":           "Фрирен",
			"f[types]":            "TV",
			"f[years][from_year]": "2023",
			"f[years][to_year]":   "2024",
			"f[sorting]":          "RATING_DESC",
			"f[publish_statuses]": "IS_NOT_ONGOING",
		}
		for key, want := range checks {
			if got := r.URL.Query().Get(key); got != want {
				t.Fatalf("query %s: got %q, want %q", key, got, want)
			}
		}
		fmt.Fprint(w, `{"data":[{"id":9542,"year":2023,"type":{"value":"TV","description":"ТВ"},"name":{"main":"Провожающая в последний путь Фрирен"}}],"meta":{"pagination":{"total":1,"total_pages":1}}}`)
	}))
	defer server.Close()

	client := New(server.URL, 2*time.Second)
	response, err := client.Catalog(context.Background(), CatalogFilter{
		Search: "Фрирен", Type: "TV", YearFrom: 2023, YearTo: 2024,
		Sorting: "RATING_DESC", PublishStatus: "IS_NOT_ONGOING",
	})
	if err != nil || len(response.Data) != 1 || response.Data[0].ID != 9542 {
		t.Fatalf("unexpected catalog result: %#v, %v", response, err)
	}
}
