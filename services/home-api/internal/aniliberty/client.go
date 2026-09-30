package aniliberty

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Client struct {
	baseURL string
	http    *http.Client
	mu      sync.Mutex
	last    time.Time
}

type Release struct {
	ID            int64  `json:"id"`
	Year          int    `json:"year"`
	Alias         string `json:"alias"`
	Description   string `json:"description"`
	EpisodesTotal int    `json:"episodes_total"`
	IsOngoing     bool   `json:"is_ongoing"`
	Type          struct {
		Value       string `json:"value"`
		Description string `json:"description"`
	} `json:"type"`
	AgeRating struct {
		Value string `json:"value"`
		Label string `json:"label"`
	} `json:"age_rating"`
	Genres []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"genres"`
	Name struct {
		Main        string  `json:"main"`
		English     string  `json:"english"`
		Alternative *string `json:"alternative"`
	} `json:"name"`
	Poster struct {
		Src       string `json:"src"`
		Preview   string `json:"preview"`
		Thumbnail string `json:"thumbnail"`
		Optimized struct {
			Src       string `json:"src"`
			Preview   string `json:"preview"`
			Thumbnail string `json:"thumbnail"`
		} `json:"optimized"`
	} `json:"poster"`
}

type CatalogFilter struct {
	Search        string
	Type          string
	YearFrom      int
	YearTo        int
	Sorting       string
	PublishStatus string
}

type CatalogResponse struct {
	Data []Release `json:"data"`
	Meta struct {
		Pagination struct {
			Total      int `json:"total"`
			TotalPages int `json:"total_pages"`
		} `json:"pagination"`
	} `json:"meta"`
}

type Torrent struct {
	ID          int64  `json:"id"`
	Hash        string `json:"hash"`
	Size        int64  `json:"size"`
	Label       string `json:"label"`
	Magnet      string `json:"magnet"`
	Seeders     int    `json:"seeders"`
	Leechers    int    `json:"leechers"`
	Description string `json:"description"`
	IsHardSub   bool   `json:"is_hardsub"`
	Quality     struct {
		Value       string `json:"value"`
		Description string `json:"description"`
	} `json:"quality"`
	Codec struct {
		Value string `json:"value"`
		Label string `json:"label"`
	} `json:"codec"`
}

func New(baseURL string, timeout time.Duration, bindAddress ...string) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if len(bindAddress) > 0 {
		if ip := net.ParseIP(bindAddress[0]); ip != nil {
			transport.DialContext = (&net.Dialer{LocalAddr: &net.TCPAddr{IP: ip}}).DialContext
		}
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout, Transport: transport},
	}
}

func (c *Client) Search(ctx context.Context, query string) ([]Release, error) {
	query = strings.TrimSpace(query)
	if len([]rune(query)) < 2 {
		return nil, errors.New("поисковый запрос должен содержать минимум 2 символа")
	}
	values := url.Values{"query": []string{query}}
	var releases []Release
	if err := c.getJSON(ctx, "/app/search/releases?"+values.Encode(), &releases); err != nil {
		return nil, err
	}
	if len(releases) > 30 {
		releases = releases[:30]
	}
	return releases, nil
}

func (c *Client) Catalog(ctx context.Context, filter CatalogFilter) (CatalogResponse, error) {
	filter.Search = strings.TrimSpace(filter.Search)
	if len([]rune(filter.Search)) < 2 {
		return CatalogResponse{}, errors.New("поисковый запрос должен содержать минимум 2 символа")
	}
	values := url.Values{"f[search]": []string{filter.Search}, "limit": []string{"30"}}
	if filter.Type != "" {
		values.Set("f[types]", filter.Type)
	}
	if filter.YearFrom > 0 {
		values.Set("f[years][from_year]", strconv.Itoa(filter.YearFrom))
	}
	if filter.YearTo > 0 {
		values.Set("f[years][to_year]", strconv.Itoa(filter.YearTo))
	}
	if filter.Sorting != "" {
		values.Set("f[sorting]", filter.Sorting)
	}
	if filter.PublishStatus != "" {
		values.Set("f[publish_statuses]", filter.PublishStatus)
	}
	var response CatalogResponse
	if err := c.getJSON(ctx, "/anime/catalog/releases?"+values.Encode(), &response); err != nil {
		return CatalogResponse{}, err
	}
	return response, nil
}

func (c *Client) Torrents(ctx context.Context, releaseID int64) ([]Torrent, error) {
	if releaseID <= 0 {
		return nil, errors.New("invalid release id")
	}
	var torrents []Torrent
	path := "/anime/torrents/release/" + strconv.FormatInt(releaseID, 10)
	if err := c.getJSON(ctx, path, &torrents); err != nil {
		return nil, err
	}
	return torrents, nil
}

func (c *Client) getJSON(ctx context.Context, path string, target any) error {
	c.rateLimit()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "HomeNode/0.1 (+local home server)")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("AniLiberty request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("AniLiberty returned HTTP %d", resp.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 8<<20))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode AniLiberty response: %w", err)
	}
	return nil
}

func (c *Client) rateLimit() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if wait := 300*time.Millisecond - time.Since(c.last); wait > 0 {
		time.Sleep(wait)
	}
	c.last = time.Now()
}
