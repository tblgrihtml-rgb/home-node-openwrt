package music

// The verification and request signature protocol follows SpotiFLAC's MIT-licensed
// community client (spotbye/SpotiFLAC, v7.2.2). This is a private integration,
// not an official API; the provider can change or discontinue it at any time.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	verifyBase       = "https://verify.spotbye.qzz.io"
	downloadAPI      = "https://tdl-oss.spotbye.qzz.io/api/dl"
	communityVersion = "7.2.2"
	manualCallback   = "http://127.0.0.1:53672/session-grant"
)

var ErrVerificationRequired = errors.New("требуется подтверждение доступа к источнику музыки")

type sessionRecord struct {
	InstallID     string `json:"install_id"`
	SessionID     string `json:"session_id,omitempty"`
	SessionSecret string `json:"session_secret,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
}

type Client struct {
	http         *http.Client
	mu           sync.Mutex
	sessionPath  string
	record       sessionRecord
	pendingState string
	pendingUntil time.Time
}

func New(sessionPath string) (*Client, error) {
	publicHTTP := mediaHTTPClient()
	client := &Client{http: &http.Client{Transport: publicHTTP.Transport, CheckRedirect: publicHTTP.CheckRedirect, Timeout: 20 * time.Second}, sessionPath: sessionPath}
	var err error
	if data, err := os.ReadFile(sessionPath); err == nil {
		if err := json.Unmarshal(data, &client.record); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if client.record.InstallID == "" {
		client.record.InstallID, err = randomHex(16)
		if err != nil {
			return nil, err
		}
		if err := client.saveLocked(); err != nil {
			return nil, err
		}
	}
	return client, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (c *Client) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(c.sessionPath), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(c.record)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(c.sessionPath), ".music-session-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), c.sessionPath)
}

func (c *Client) validLocked() bool {
	expires, err := time.Parse(time.RFC3339Nano, c.record.ExpiresAt)
	return err == nil && c.record.SessionID != "" && c.record.SessionSecret != "" && time.Until(expires) > 5*time.Minute
}

func (c *Client) Verified() (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.validLocked(), c.record.ExpiresAt
}

func (c *Client) StartVerification(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, err := randomHex(24)
	if err != nil {
		return "", err
	}
	u, _ := url.Parse(verifyBase + "/bootstrap")
	q := u.Query()
	q.Set("install_id", c.record.InstallID)
	q.Set("app_version", communityVersion)
	q.Set("platform", "desktop")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("verification bootstrap HTTP %d", resp.StatusCode)
	}
	var data struct {
		ChallengeURL string `json:"challenge_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32*1024)).Decode(&data); err != nil {
		return "", err
	}
	challenge, err := url.Parse(data.ChallengeURL)
	if err != nil || challenge.Scheme != "https" || challenge.Hostname() != "verify.spotbye.qzz.io" {
		return "", errors.New("invalid verification challenge URL")
	}
	// The provider accepts only a loopback callback. HomeNode runs on a router,
	// so the browser copies the resulting URL back to the authenticated app.
	callback := manualCallback + "?state=" + state
	q = challenge.Query()
	q.Set("cb", callback)
	challenge.RawQuery = q.Encode()
	c.pendingState, c.pendingUntil = state, time.Now().Add(5*time.Minute)
	return challenge.String(), nil
}

func (c *Client) CompleteVerificationURL(ctx context.Context, callbackURL string) error {
	if len(callbackURL) > 8192 {
		return errors.New("verification URL is too long")
	}
	u, err := url.Parse(strings.TrimSpace(callbackURL))
	if err != nil || u.Scheme != "http" || u.Host != "127.0.0.1:53672" || u.Path != "/session-grant" || u.User != nil || u.Fragment != "" {
		return errors.New("invalid verification URL")
	}
	q := u.Query()
	if len(q["state"]) != 1 || len(q["grant"]) != 1 {
		return errors.New("incomplete verification URL")
	}
	return c.CompleteVerification(ctx, q.Get("state"), q.Get("grant"))
}

func (c *Client) CompleteVerification(ctx context.Context, state, grant string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(grant) == 0 || len(grant) > 4096 || time.Now().After(c.pendingUntil) ||
		!hmac.Equal([]byte(state), []byte(c.pendingState)) || c.pendingState == "" {
		return errors.New("invalid or expired verification callback")
	}
	c.pendingState = "" // one-time callback
	payload, _ := json.Marshal(map[string]string{
		"grant": grant, "install_id": c.record.InstallID,
		"app_version": communityVersion, "platform": "desktop",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, verifyBase+"/session/exchange", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("session exchange HTTP %d", resp.StatusCode)
	}
	var exchanged sessionRecord
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32*1024)).Decode(&exchanged); err != nil {
		return err
	}
	if exchanged.SessionID == "" || exchanged.SessionSecret == "" || exchanged.ExpiresAt == "" {
		return errors.New("incomplete verification session")
	}
	c.record.SessionID, c.record.SessionSecret, c.record.ExpiresAt = exchanged.SessionID, exchanged.SessionSecret, exchanged.ExpiresAt
	return c.saveLocked()
}

func (c *Client) sign(req *http.Request, body []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.validLocked() {
		return ErrVerificationRequired
	}
	nonce, err := randomHex(12)
	if err != nil {
		return err
	}
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	parsed, _ := time.Parse("2006-01-02T15:04:05.000Z", timestamp)
	rolling := hmacSHA([]byte(c.record.SessionSecret), []byte(fmt.Sprintf("%d:%s", parsed.Unix()/300, c.record.SessionID)))
	sum := sha256.Sum256(body)
	bodyHash := hex.EncodeToString(sum[:])
	input := strings.Join([]string{"SPOTIFLAC-HMAC-V1", req.Method, req.URL.EscapedPath(), "", bodyHash,
		timestamp, nonce, c.record.SessionID, communityVersion, "desktop"}, "\n")
	req.Header.Set("X-Sig-Session", c.record.SessionID)
	req.Header.Set("X-Sig-Timestamp", timestamp)
	req.Header.Set("X-Sig-Nonce", nonce)
	req.Header.Set("X-Sig-Body-SHA256", bodyHash)
	req.Header.Set("X-Sig-Signature", base64.RawURLEncoding.EncodeToString(hmacSHA(rolling, []byte(input))))
	req.Header.Set("X-Sig-App-Version", communityVersion)
	req.Header.Set("X-Sig-Platform", "desktop")
	return nil
}

func hmacSHA(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(data)
	return h.Sum(nil)
}

func (c *Client) clearSession() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.record.SessionID, c.record.SessionSecret, c.record.ExpiresAt = "", "", ""
	_ = c.saveLocked()
}
