package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"homenode/services/home-api/internal/webui"
)

const sessionCookieName = "homenode_session"

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if s.authenticated(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.servePublicAsset(w, "static/login.html", "text/html; charset=utf-8")
}

func (s *Server) loginScript(w http.ResponseWriter, _ *http.Request) {
	s.servePublicAsset(w, "static/login.js", "text/javascript; charset=utf-8")
}

func (s *Server) themeScript(w http.ResponseWriter, _ *http.Request) {
	s.servePublicAsset(w, "static/theme.js", "text/javascript; charset=utf-8")
}

func (s *Server) stylesheet(w http.ResponseWriter, _ *http.Request) {
	s.servePublicAsset(w, "static/styles.css", "text/css; charset=utf-8")
}

func (s *Server) servePublicAsset(w http.ResponseWriter, path, contentType string) {
	data, err := webui.Files.ReadFile(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "Файл не найден")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Remember bool   `json:"remember"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !secureEqual(strings.TrimSpace(input.Username), s.cfg.APIUsername) || !secureEqual(input.Password, s.cfg.APIPassword) {
		time.Sleep(250 * time.Millisecond)
		writeError(w, http.StatusUnauthorized, "Неверный логин или пароль")
		return
	}
	s.setSessionCookie(w, input.Remember)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) logout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) authenticated(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return false
	}
	expected := s.signSession(parts[0])
	provided, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(expected, provided) {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	fields := strings.Split(string(payload), "|")
	if len(fields) != 3 || fields[0] != "v1" || !secureEqual(fields[1], s.cfg.APIUsername) {
		return false
	}
	expires, err := strconv.ParseInt(fields[2], 10, 64)
	return err == nil && time.Now().Unix() < expires
}

func (s *Server) setSessionCookie(w http.ResponseWriter, remember bool) {
	duration := 12 * time.Hour
	if remember {
		duration = 180 * 24 * time.Hour
	}
	expires := time.Now().Add(duration)
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("v1|%s|%d", s.cfg.APIUsername, expires.Unix())))
	signature := base64.RawURLEncoding.EncodeToString(s.signSession(payload))
	cookie := &http.Cookie{
		Name: sessionCookieName, Value: payload + "." + signature, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
	}
	if remember {
		cookie.Expires = expires
		cookie.MaxAge = int(duration.Seconds())
	}
	http.SetCookie(w, cookie)
}

func (s *Server) signSession(payload string) []byte {
	key := []byte(s.cfg.SessionSecret + "\x00" + s.cfg.APIPassword)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(payload))
	return mac.Sum(nil)
}
