package music

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"homenode/services/home-api/internal/store"
)

const maxMusicBytes int64 = 250 * 1024 * 1024

type RetryError struct {
	Delay time.Duration
	Code  int
}

func (e *RetryError) Error() string {
	return fmt.Sprintf("источник временно недоступен (HTTP %d)", e.Code)
}

func retryDelay(resp *http.Response) time.Duration {
	if raw := resp.Header.Get("Retry-After"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			return min(time.Duration(n)*time.Second, 24*time.Hour)
		}
		if when, err := http.ParseTime(raw); err == nil {
			return min(max(time.Until(when), time.Second), 24*time.Hour)
		}
	}
	if raw := resp.Header.Get("X-RateLimit-Reset"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return min(max(time.Until(time.Unix(n, 0)), time.Second), 24*time.Hour)
		}
	}
	return 30 * time.Minute
}

func (c *Client) mediaURL(ctx context.Context, tidalID int64) (string, error) {
	payload, _ := json.Marshal(map[string]string{"id": strconv.FormatInt(tidalID, 10), "quality": "16"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, downloadAPI, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if err := c.sign(req, payload); err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusPreconditionRequired:
		c.clearSession()
		return "", ErrVerificationRequired
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return "", &RetryError{Delay: retryDelay(resp), Code: resp.StatusCode}
	default:
		return "", fmt.Errorf("music provider HTTP %d", resp.StatusCode)
	}
	var data struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&data); err != nil {
		return "", err
	}
	if data.URL == "" {
		return "", errors.New("music provider returned no file URL")
	}
	return data.URL, nil
}

func safePublicIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return false
	}
	return !netip.MustParsePrefix("100.64.0.0/10").Contains(addr)
}

func publicMediaURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return nil, errors.New("provider returned an unsafe media URL")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && !safePublicIP(ip) {
		return nil, errors.New("provider media URL points to a private network")
	}
	return u, nil
}

func mediaHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{MaxIdleConns: 2, IdleConnTimeout: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ip := range ips {
			if safePublicIP(ip) {
				conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, errors.New("media host has no public IP address")
	}
	return &http.Client{Transport: transport, Timeout: 20 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 4 {
			return errors.New("too many media redirects")
		}
		_, err := publicMediaURL(req.URL.String())
		return err
	}}
}

func safeName(value string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(value) {
		if unicode.IsControl(r) || strings.ContainsRune("/\\:*?\"<>|", r) {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
		if b.Len() >= 120 {
			break
		}
	}
	name := strings.Trim(strings.Join(strings.Fields(b.String()), " "), ". ")
	if name == "" || name == ".." {
		return "Без названия"
	}
	return name
}

func mountedMediaRoot(path string) bool {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	path = filepath.Clean(path)
	longest := ""
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) != 2 {
			continue
		}
		fields := strings.Fields(parts[0])
		if len(fields) < 5 {
			continue
		}
		mount := strings.ReplaceAll(fields[4], "\\040", " ")
		if (path == mount || strings.HasPrefix(path, mount+string(os.PathSeparator))) && len(mount) > len(longest) {
			longest = mount
		}
	}
	return longest != "" && longest != "/" && longest != "/overlay" && longest != "/tmp"
}

func ensureRealDir(path string) error {
	if err := os.Mkdir(path, 0750); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("music destination is not a real directory")
	}
	return nil
}

func (c *Client) Download(ctx context.Context, job store.MusicJob, musicRoot string) (string, error) {
	if !mountedMediaRoot(musicRoot) {
		return "", errors.New("SSD не смонтирован; загрузка остановлена для защиты памяти роутера")
	}
	if err := ensureRealDir(musicRoot); err != nil {
		return "", err
	}
	resolvedRoot, err := filepath.EvalSymlinks(musicRoot)
	if err != nil || resolvedRoot != filepath.Clean(musicRoot) {
		return "", errors.New("music directory must not use symbolic links")
	}
	id, err := c.tidalID(ctx, job.AppleTrackID)
	if err != nil {
		return "", err
	}
	rawURL, err := c.mediaURL(ctx, id)
	if err != nil {
		return "", err
	}
	u, err := publicMediaURL(rawURL)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	client := mediaHTTPClient()
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == 429 || resp.StatusCode == 503 {
			return "", &RetryError{Delay: retryDelay(resp), Code: resp.StatusCode}
		}
		return "", fmt.Errorf("music file HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxMusicBytes {
		return "", errors.New("music file exceeds size limit")
	}
	f, err := os.CreateTemp(musicRoot, ".music-part-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0640); err != nil {
		return "", err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxMusicBytes+1))
	if err != nil {
		return "", err
	}
	if n > maxMusicBytes || n < 1024 {
		return "", errors.New("music file is too large or incomplete")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(f, header); err != nil || string(header) != "fLaC" {
		return "", errors.New("provider returned a non-FLAC file")
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	artist, album := safeName(job.Artist), safeName(job.Album)
	artistDir := filepath.Join(musicRoot, artist)
	if err := ensureRealDir(artistDir); err != nil {
		return "", err
	}
	albumDir := filepath.Join(artistDir, album)
	if err := ensureRealDir(albumDir); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s [%d].flac", safeName(job.Title), job.AppleTrackID)
	destination := filepath.Join(albumDir, name)
	if _, err := os.Lstat(destination); err == nil {
		return "", errors.New("track already exists on disk")
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if !mountedMediaRoot(musicRoot) {
		return "", errors.New("SSD detached during download")
	}
	// Linking fails if the target appeared meanwhile; never overwrite a user's file.
	if err := os.Link(f.Name(), destination); err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join("music", artist, album, name)), nil
}
