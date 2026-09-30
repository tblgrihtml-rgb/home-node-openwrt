package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"homenode/services/home-api/internal/aniliberty"
	"homenode/services/home-api/internal/rutracker"
	"homenode/services/home-api/internal/transmission"
)

type localClient struct {
	baseURL string
	user    string
	pass    string
	http    *http.Client
}

type libraryTitle struct {
	Title     string `json:"title"`
	Category  string `json:"category"`
	FileCount int    `json:"file_count"`
	Size      int64  `json:"size"`
}

func newLocalClient(baseURL, user, pass string) *localClient {
	return &localClient{baseURL: baseURL, user: user, pass: pass, http: &http.Client{Timeout: 25 * time.Second}}
}

func (c *localClient) request(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.user, c.pass)
	req.Header.Set("X-HomeNode-Request", "1")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("Home Node API недоступен")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiError struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&apiError)
		if apiError.Error == "" {
			apiError.Error = fmt.Sprintf("Home Node HTTP %d", resp.StatusCode)
		}
		return errors.New(apiError.Error)
	}
	if output != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(output)
	}
	return nil
}

func (c *localClient) searchAnime(ctx context.Context, query string) ([]aniliberty.Release, error) {
	var response aniliberty.CatalogResponse
	err := c.request(ctx, http.MethodGet, "/api/anime/catalog?q="+url.QueryEscape(query)+"&sorting=RATING_DESC", nil, &response)
	return response.Data, err
}

func (c *localClient) searchTracker(ctx context.Context, query string) ([]rutracker.Result, error) {
	var items []rutracker.Result
	err := c.request(ctx, http.MethodGet, "/api/tracker/search?q="+url.QueryEscape(query), nil, &items)
	return items, err
}

func (c *localClient) animeTorrents(ctx context.Context, releaseID int64) ([]aniliberty.Torrent, error) {
	var items []aniliberty.Torrent
	err := c.request(ctx, http.MethodGet, "/api/anime/"+strconv.FormatInt(releaseID, 10)+"/torrents", nil, &items)
	return items, err
}

func (c *localClient) downloadAnime(ctx context.Context, releaseID, torrentID int64, title string) error {
	return c.request(ctx, http.MethodPost, "/api/anime/download", map[string]any{
		"release_id": releaseID, "torrent_id": torrentID, "title": title,
	}, nil)
}

func (c *localClient) downloadTracker(ctx context.Context, topicID int64, title string) error {
	return c.request(ctx, http.MethodPost, "/api/tracker/download", map[string]any{
		"topic_id": topicID, "title": title,
	}, nil)
}

func (c *localClient) downloads(ctx context.Context) ([]transmission.Torrent, error) {
	var items []transmission.Torrent
	err := c.request(ctx, http.MethodGet, "/api/downloads", nil, &items)
	return items, err
}

func (c *localClient) control(ctx context.Context, id int64, action string) error {
	method := http.MethodPost
	path := "/api/downloads/" + strconv.FormatInt(id, 10) + "/" + action
	if action == "remove" {
		method, path = http.MethodDelete, "/api/downloads/"+strconv.FormatInt(id, 10)
	}
	if action == "remove-data" {
		method, path = http.MethodDelete, "/api/downloads/"+strconv.FormatInt(id, 10)+"/data"
	}
	return c.request(ctx, method, path, nil, nil)
}

func (c *localClient) library(ctx context.Context) ([]libraryTitle, error) {
	var items []libraryTitle
	err := c.request(ctx, http.MethodGet, "/api/library", nil, &items)
	return items, err
}
