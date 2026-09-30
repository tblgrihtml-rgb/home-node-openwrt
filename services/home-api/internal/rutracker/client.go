package rutracker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/text/encoding/charmap"
)

var ErrNotConfigured = errors.New("RuTracker не настроен: добавьте логин и пароль в секретную конфигурацию")

type Client struct {
	baseURL  string
	username string
	password string
	http     *http.Client
	mu       sync.Mutex
	loggedIn bool
}

type Result struct {
	TopicID  int64  `json:"topic_id"`
	Title    string `json:"title"`
	Forum    string `json:"forum"`
	Size     string `json:"size"`
	Seeders  int    `json:"seeders"`
	Leechers int    `json:"leechers"`
}

func New(baseURL, proxyURL, username, password string, timeout time.Duration, bindAddress ...string) *Client {
	jar, _ := cookiejar.New(nil)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if len(bindAddress) > 0 {
		if ip := net.ParseIP(bindAddress[0]); ip != nil {
			transport.DialContext = (&net.Dialer{LocalAddr: &net.TCPAddr{IP: ip}}).DialContext
		}
	}
	if proxyURL != "" {
		if parsed, err := url.Parse(proxyURL); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" {
			transport.Proxy = http.ProxyURL(parsed)
		}
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"), username: username, password: password,
		http: &http.Client{Timeout: timeout, Jar: jar, Transport: transport},
	}
}

func (c *Client) Configured() bool { return c.username != "" && c.password != "" }

func (c *Client) Search(ctx context.Context, query string) ([]Result, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	if err := c.login(ctx); err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if len([]rune(query)) < 2 {
		return nil, errors.New("поисковый запрос должен содержать минимум 2 символа")
	}
	encodedQuery, err := charmap.Windows1251.NewEncoder().String(query)
	if err != nil {
		return nil, errors.New("RuTracker не поддерживает символы поискового запроса")
	}
	form := url.Values{"nm": []string{encodedQuery}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/forum/tracker.php", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 HomeNode/0.1")
	resp, err := c.do(req)
	if err != nil {
		return nil, errors.New("RuTracker временно недоступен через служебный прокси; повторите поиск")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
		return nil, responseError("поиск", resp.StatusCode, body)
	}
	reader := io.Reader(resp.Body)
	if bytesContainFold([]byte(resp.Header.Get("Content-Type")), "windows-1251") {
		reader = charmap.Windows1251.NewDecoder().Reader(reader)
	}
	return ParseSearch(reader)
}

func (c *Client) Download(ctx context.Context, topicID int64) ([]byte, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	if topicID <= 0 {
		return nil, errors.New("invalid topic id")
	}
	if err := c.login(ctx); err != nil {
		return nil, err
	}
	u := fmt.Sprintf("%s/forum/dl.php?t=%d", c.baseURL, topicID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 HomeNode/0.1")
	resp, err := c.do(req)
	if err != nil {
		return nil, errors.New("RuTracker временно недоступен через служебный прокси; повторите загрузку")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
		return nil, responseError("загрузка торрента", resp.StatusCode, body)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 8<<20 || len(data) < 10 || data[0] != 'd' {
		return nil, errors.New("RuTracker returned an invalid torrent file")
	}
	return data, nil
}

func (c *Client) login(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loggedIn {
		return nil
	}
	form := url.Values{
		"login_username": []string{c.username},
		"login_password": []string{c.password},
		"login":          []string{"Вход"},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/forum/login.php", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 HomeNode/0.1")
	resp, err := c.do(req)
	if err != nil {
		return errors.New("RuTracker временно недоступен через служебный прокси; повторите вход")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return responseError("вход", resp.StatusCode, body)
	}
	if bytesContainFold(body, "login_username") {
		return errors.New("RuTracker не принял вход: проверьте учётные данные или наличие CAPTCHA")
	}
	c.loggedIn = true
	return nil
}

// do retries one transient proxy/network failure. The official RuTracker
// extension proxy occasionally drops a TLS handshake, while the next request
// normally succeeds. Request bodies created from strings.Reader are replayable.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		current := req.Clone(req.Context())
		if req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			current.Body = body
		}
		resp, err := c.http.Do(current)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if req.Context().Err() != nil || attempt == 1 {
			break
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-req.Context().Done():
			timer.Stop()
			return nil, req.Context().Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

var topicPattern = regexp.MustCompile(`(?:\?|&)t=(\d+)`)

func ParseSearch(reader io.Reader) ([]Result, error) {
	doc, err := goquery.NewDocumentFromReader(io.LimitReader(reader, 8<<20))
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, 30)
	doc.Find("#tor-tbl tbody tr").EachWithBreak(func(_ int, row *goquery.Selection) bool {
		link := row.Find("a.tLink").First()
		href, ok := link.Attr("href")
		if !ok {
			return true
		}
		match := topicPattern.FindStringSubmatch(href)
		if len(match) != 2 {
			return true
		}
		id, _ := strconv.ParseInt(match[1], 10, 64)
		result := Result{
			TopicID:  id,
			Title:    clean(link.Text()),
			Forum:    clean(row.Find("a.f-name").First().Text()),
			Size:     clean(row.Find("td.tor-size").First().Text()),
			Seeders:  parseInt(row.Find("td.seedmed").First().Text()),
			Leechers: parseInt(row.Find("td.leechmed").First().Text()),
		}
		if result.Title != "" {
			results = append(results, result)
		}
		return len(results) < 30
	})
	return results, nil
}

func clean(value string) string { return strings.Join(strings.Fields(value), " ") }
func parseInt(value string) int { n, _ := strconv.Atoi(strings.TrimSpace(value)); return n }
func bytesContainFold(haystack []byte, needle string) bool {
	return strings.Contains(strings.ToLower(string(haystack)), strings.ToLower(needle))
}

func responseError(action string, status int, body []byte) error {
	if status == http.StatusForbidden || bytesContainFold(body, "just a moment") || bytesContainFold(body, "cf-chl-") {
		return fmt.Errorf("RuTracker отклонил запрос через Cloudflare при операции «%s»; служебный прокси требует обновления", action)
	}
	return fmt.Errorf("RuTracker вернул HTTP %d при операции «%s»", status, action)
}
