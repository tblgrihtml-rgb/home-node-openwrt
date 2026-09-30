package transmission

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

type Client struct {
	url      string
	username string
	password string
	http     *http.Client
	mu       sync.RWMutex
	session  string
}

type Torrent struct {
	ID             int64   `json:"id"`
	Name           string  `json:"name"`
	Status         int     `json:"status"`
	PercentDone    float64 `json:"percentDone"`
	RateDownload   int64   `json:"rateDownload"`
	RateUpload     int64   `json:"rateUpload"`
	ETA            int64   `json:"eta"`
	DownloadDir    string  `json:"downloadDir"`
	ErrorString    string  `json:"errorString"`
	PeersConnected int     `json:"peersConnected"`
	IsStalled      bool    `json:"isStalled"`
	TotalSize      int64   `json:"totalSize"`
	LeftUntilDone  int64   `json:"leftUntilDone"`
	AddedDate      int64   `json:"addedDate"`
	UploadedEver   int64   `json:"uploadedEver"`
}

type TorrentFile struct {
	Name string `json:"name"`
}

type TorrentLayout struct {
	ID                      int64         `json:"id"`
	Name                    string        `json:"name"`
	MetadataPercentComplete float64       `json:"metadataPercentComplete"`
	Files                   []TorrentFile `json:"files"`
}

func New(rpcURL, username, password string, timeout time.Duration) *Client {
	return &Client{
		url: rpcURL, username: username, password: password,
		http: &http.Client{Timeout: timeout},
	}
}

func (c *Client) AddMagnet(ctx context.Context, magnet, downloadDir string) (map[string]any, error) {
	if len(magnet) < 20 || magnet[:8] != "magnet:?" {
		return nil, errors.New("invalid magnet link")
	}
	return c.add(ctx, map[string]any{"filename": magnet, "download-dir": downloadDir})
}

func (c *Client) AddTorrent(ctx context.Context, torrent []byte, downloadDir string) (map[string]any, error) {
	if len(torrent) < 10 || len(torrent) > 8<<20 {
		return nil, errors.New("invalid torrent file size")
	}
	return c.add(ctx, map[string]any{
		"metainfo":     base64.StdEncoding.EncodeToString(torrent),
		"download-dir": downloadDir,
	})
}

func (c *Client) add(ctx context.Context, args map[string]any) (map[string]any, error) {
	var result map[string]any
	if err := c.call(ctx, "torrent-add", args, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) List(ctx context.Context) ([]Torrent, error) {
	var result struct {
		Torrents []Torrent `json:"torrents"`
	}
	fields := []string{"id", "name", "status", "percentDone", "rateDownload", "rateUpload", "eta", "downloadDir", "errorString", "peersConnected", "isStalled", "totalSize", "leftUntilDone", "addedDate", "uploadedEver"}
	if err := c.call(ctx, "torrent-get", map[string]any{"fields": fields}, &result); err != nil {
		return nil, err
	}
	return result.Torrents, nil
}

func (c *Client) Start(ctx context.Context, id int64) error {
	return c.control(ctx, "torrent-start", id, nil)
}

func (c *Client) Stop(ctx context.Context, id int64) error {
	return c.control(ctx, "torrent-stop", id, nil)
}

func (c *Client) Remove(ctx context.Context, id int64, deleteData bool) error {
	return c.control(ctx, "torrent-remove", id, map[string]any{"delete-local-data": deleteData})
}

func (c *Client) Layout(ctx context.Context, id int64) (TorrentLayout, error) {
	if id <= 0 {
		return TorrentLayout{}, errors.New("invalid torrent id")
	}
	var result struct {
		Torrents []TorrentLayout `json:"torrents"`
	}
	fields := []string{"id", "name", "metadataPercentComplete", "files"}
	if err := c.call(ctx, "torrent-get", map[string]any{"ids": []int64{id}, "fields": fields}, &result); err != nil {
		return TorrentLayout{}, err
	}
	if len(result.Torrents) != 1 {
		return TorrentLayout{}, errors.New("torrent not found")
	}
	return result.Torrents[0], nil
}

func (c *Client) RenamePath(ctx context.Context, id int64, path, name string) error {
	if id <= 0 || path == "" || name == "" {
		return errors.New("invalid rename arguments")
	}
	return c.call(ctx, "torrent-rename-path", map[string]any{
		"ids": []int64{id}, "path": path, "name": name,
	}, nil)
}

func (c *Client) SetLocation(ctx context.Context, id int64, location string, move bool) error {
	if id <= 0 || location == "" {
		return errors.New("invalid location arguments")
	}
	return c.call(ctx, "torrent-set-location", map[string]any{
		"ids": []int64{id}, "location": location, "move": move,
	}, nil)
}

func (c *Client) control(ctx context.Context, method string, id int64, extra map[string]any) error {
	if id <= 0 {
		return errors.New("invalid torrent id")
	}
	args := map[string]any{"ids": []int64{id}}
	for key, value := range extra {
		args[key] = value
	}
	return c.call(ctx, method, args, nil)
}

func (c *Client) call(ctx context.Context, method string, args any, target any) error {
	payload, err := json.Marshal(map[string]any{"method": method, "arguments": args})
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		c.mu.RLock()
		session := c.session
		c.mu.RUnlock()
		if session != "" {
			req.Header.Set("X-Transmission-Session-Id", session)
		}
		req.SetBasicAuth(c.username, c.password)

		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("Transmission RPC: %w", err)
		}
		if resp.StatusCode == http.StatusConflict {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			c.mu.Lock()
			c.session = resp.Header.Get("X-Transmission-Session-Id")
			c.mu.Unlock()
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("Transmission returned HTTP %d", resp.StatusCode)
		}
		var response struct {
			Arguments json.RawMessage `json:"arguments"`
			Result    string          `json:"result"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&response); err != nil {
			return err
		}
		if response.Result != "success" {
			return fmt.Errorf("Transmission: %s", response.Result)
		}
		if target != nil && len(response.Arguments) > 0 {
			if err := json.Unmarshal(response.Arguments, target); err != nil {
				return err
			}
		}
		return nil
	}
	return errors.New("Transmission session negotiation failed")
}
