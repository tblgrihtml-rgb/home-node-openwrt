package music

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type Track struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Album  string `json:"album"`
}

type appleResponse struct {
	Results []struct {
		TrackID        int64  `json:"trackId"`
		Kind           string `json:"kind"`
		TrackName      string `json:"trackName"`
		ArtistName     string `json:"artistName"`
		CollectionName string `json:"collectionName"`
	} `json:"results"`
}

func (c *Client) apple(ctx context.Context, endpoint string, q url.Values) ([]Track, error) {
	u := "https://itunes.apple.com/" + endpoint + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("music catalog HTTP %d", resp.StatusCode)
	}
	var data appleResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&data); err != nil {
		return nil, err
	}
	tracks := make([]Track, 0, len(data.Results))
	for _, item := range data.Results {
		if item.Kind != "song" || item.TrackID <= 0 || item.TrackName == "" || item.ArtistName == "" {
			continue
		}
		tracks = append(tracks, Track{ID: item.TrackID, Title: item.TrackName, Artist: item.ArtistName, Album: item.CollectionName})
	}
	return tracks, nil
}

func (c *Client) Search(ctx context.Context, query string) ([]Track, error) {
	query = strings.TrimSpace(query)
	if len([]rune(query)) < 2 || len([]rune(query)) > 100 {
		return nil, errors.New("запрос должен содержать от 2 до 100 символов")
	}
	q := url.Values{"term": {query}, "media": {"music"}, "entity": {"song"}, "limit": {"20"}}
	return c.apple(ctx, "search", q)
}

func (c *Client) Lookup(ctx context.Context, id int64) (Track, error) {
	if id <= 0 {
		return Track{}, errors.New("invalid track id")
	}
	tracks, err := c.apple(ctx, "lookup", url.Values{"id": {strconv.FormatInt(id, 10)}, "entity": {"song"}})
	if err != nil {
		return Track{}, err
	}
	for _, track := range tracks {
		if track.ID == id {
			return track, nil
		}
	}
	return Track{}, errors.New("трек не найден в каталоге")
}

var songLinkData = regexp.MustCompile(`(?is)<script id="__NEXT_DATA__" type="application/json">(.*?)</script>`)

func (c *Client) tidalID(ctx context.Context, appleTrackID int64) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("https://song.link/i/%d", appleTrackID), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 HomeNode/0.1")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("track resolver HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return 0, err
	}
	match := songLinkData.FindSubmatch(body)
	if len(match) != 2 {
		return 0, errors.New("track resolver changed its page format")
	}
	var data struct {
		Props struct {
			PageProps struct {
				PageData struct {
					Sections []struct {
						Links []struct {
							Platform string `json:"platform"`
							URL      string `json:"url"`
						} `json:"links"`
					} `json:"sections"`
				} `json:"pageData"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if err := json.Unmarshal(match[1], &data); err != nil {
		return 0, err
	}
	for _, section := range data.Props.PageProps.PageData.Sections {
		for _, link := range section.Links {
			if link.Platform != "tidal" {
				continue
			}
			u, err := url.Parse(link.URL)
			if err != nil || u.Scheme != "https" || (u.Hostname() != "tidal.com" && u.Hostname() != "www.tidal.com") {
				continue
			}
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) != 2 || parts[0] != "track" {
				continue
			}
			id, err := strconv.ParseInt(parts[1], 10, 64)
			if err == nil && id > 0 {
				return id, nil
			}
		}
	}
	return 0, errors.New("для этого трека не найден источник Tidal")
}
