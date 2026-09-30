package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"homenode/services/home-api/internal/aniliberty"
	"homenode/services/home-api/internal/config"
	"homenode/services/home-api/internal/rutracker"
	"homenode/services/home-api/internal/store"
	"homenode/services/home-api/internal/telegram"
	"homenode/services/home-api/internal/transmission"
	"homenode/services/home-api/internal/webui"
)

const version = "0.1.0"

type Server struct {
	cfg          config.Config
	anime        *aniliberty.Client
	tracker      *rutracker.Client
	transmission *transmission.Client
	store        *store.Store
	telegram     *telegram.Bot
	logger       *slog.Logger
	handler      http.Handler
}

type MediaFile struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	Category   string `json:"category"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modified_at"`
	SMBPath    string `json:"smb_path"`
}

func New(cfg config.Config, anime *aniliberty.Client, tracker *rutracker.Client, transmissionClient *transmission.Client, database *store.Store, telegramBot *telegram.Bot, logger *slog.Logger) (*Server, error) {
	s := &Server{cfg: cfg, anime: anime, tracker: tracker, transmission: transmissionClient, store: database, telegram: telegramBot, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("GET /login.js", s.loginScript)
	mux.HandleFunc("GET /theme.js", s.themeScript)
	mux.HandleFunc("GET /styles.css", s.stylesheet)
	mux.HandleFunc("POST /api/session", s.login)
	mux.Handle("POST /api/session/logout", s.auth(s.mutation(http.HandlerFunc(s.logout))))
	mux.HandleFunc("GET /health", s.health)
	mux.Handle("GET /api/status", s.auth(http.HandlerFunc(s.status)))
	mux.Handle("GET /api/system/health", s.auth(http.HandlerFunc(s.systemHealth)))
	mux.Handle("GET /api/media", s.auth(http.HandlerFunc(s.media)))
	mux.Handle("GET /api/media/stream", s.auth(http.HandlerFunc(s.mediaStream)))
	mux.Handle("GET /api/media/compatible", s.auth(http.HandlerFunc(s.mediaCompatible)))
	mux.Handle("GET /api/library", s.auth(http.HandlerFunc(s.library)))
	mux.Handle("GET /api/watch-progress", s.auth(http.HandlerFunc(s.watchProgressList)))
	mux.Handle("PUT /api/watch-progress", s.auth(s.mutation(http.HandlerFunc(s.watchProgressUpdate))))
	mux.Handle("GET /api/history", s.auth(http.HandlerFunc(s.history)))
	mux.Handle("GET /api/devices", s.auth(http.HandlerFunc(s.devices)))
	mux.Handle("POST /api/devices/{mac}/vpn", s.auth(s.mutation(http.HandlerFunc(s.deviceVPN))))
	mux.Handle("DELETE /api/devices/{mac}/vpn", s.auth(s.mutation(http.HandlerFunc(s.deviceVPN))))
	mux.Handle("GET /api/downloads", s.auth(http.HandlerFunc(s.downloads)))
	mux.Handle("POST /api/downloads/{torrentID}/start", s.auth(s.mutation(http.HandlerFunc(s.downloadStart))))
	mux.Handle("POST /api/downloads/{torrentID}/stop", s.auth(s.mutation(http.HandlerFunc(s.downloadStop))))
	mux.Handle("DELETE /api/downloads/{torrentID}/data", s.auth(s.mutation(http.HandlerFunc(s.downloadDeleteData))))
	mux.Handle("DELETE /api/downloads/{torrentID}", s.auth(s.mutation(http.HandlerFunc(s.downloadRemove))))
	mux.Handle("GET /api/anime/search", s.auth(http.HandlerFunc(s.animeSearch)))
	mux.Handle("GET /api/anime/catalog", s.auth(http.HandlerFunc(s.animeCatalog)))
	mux.Handle("GET /api/anime/{releaseID}/torrents", s.auth(http.HandlerFunc(s.animeTorrents)))
	mux.Handle("POST /api/anime/download", s.auth(s.mutation(http.HandlerFunc(s.animeDownload))))
	mux.Handle("GET /api/tracker/search", s.auth(http.HandlerFunc(s.trackerSearch)))
	mux.Handle("POST /api/tracker/download", s.auth(s.mutation(http.HandlerFunc(s.trackerDownload))))
	mux.Handle("GET /api/telegram/status", s.auth(http.HandlerFunc(s.telegramStatus)))
	mux.Handle("POST /api/telegram/pairing", s.auth(s.mutation(http.HandlerFunc(s.telegramPairing))))
	mux.Handle("DELETE /api/telegram/pairing", s.auth(s.mutation(http.HandlerFunc(s.telegramUnpair))))
	mux.Handle("POST /api/telegram/test", s.auth(s.mutation(http.HandlerFunc(s.telegramTest))))

	staticFS, err := fs.Sub(webui.Files, "static")
	if err != nil {
		return nil, err
	}
	mux.Handle("GET /", s.auth(http.FileServer(http.FS(staticFS))))
	s.handler = s.securityHeaders(mux)
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "homenode-api", "version": version})
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(s.cfg.MediaRoot, &stat); err != nil {
		writeError(w, http.StatusServiceUnavailable, "SSD недоступен")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":              version,
		"media_root":           s.cfg.MediaRoot,
		"disk_total":           int64(stat.Blocks) * int64(stat.Bsize),
		"disk_free":            int64(stat.Bavail) * int64(stat.Bsize),
		"rutracker_configured": s.tracker.Configured(),
	})
}

func (s *Server) media(w http.ResponseWriter, r *http.Request) {
	files := make([]MediaFile, 0, 100)
	err := filepath.WalkDir(s.cfg.MediaRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasPrefix(entry.Name(), "._") || strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if !isSupportedMediaExtension(ext) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(s.cfg.MediaRoot, path)
		if err != nil {
			return err
		}
		category := strings.Split(filepath.ToSlash(relative), "/")[0]
		files = append(files, MediaFile{
			Path: filepath.ToSlash(relative), Name: entry.Name(), Category: category,
			Size: info.Size(), ModifiedAt: info.ModTime().UTC().Format(time.RFC3339),
			SMBPath: "smb://192.168.1.1/HomeNode/media/" + filepath.ToSlash(relative),
		})
		if len(files) >= 2000 {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		s.logger.Error("scan media", "error", err)
		writeError(w, http.StatusInternalServerError, "Не удалось прочитать медиатеку")
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	events, err := s.store.Recent(r.Context(), 40)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось прочитать историю")
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) downloads(w http.ResponseWriter, r *http.Request) {
	items, err := s.transmission.List(r.Context())
	if err != nil {
		s.logger.Error("list Transmission downloads", "error", err)
		writeError(w, http.StatusBadGateway, "Transmission недоступен")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) downloadStart(w http.ResponseWriter, r *http.Request) {
	s.downloadControl(w, r, "start")
}

func (s *Server) downloadStop(w http.ResponseWriter, r *http.Request) {
	s.downloadControl(w, r, "stop")
}

func (s *Server) downloadRemove(w http.ResponseWriter, r *http.Request) {
	s.downloadControl(w, r, "remove")
}

func (s *Server) downloadDeleteData(w http.ResponseWriter, r *http.Request) {
	s.downloadControl(w, r, "remove-data")
}

func (s *Server) downloadControl(w http.ResponseWriter, r *http.Request, action string) {
	id, err := strconv.ParseInt(r.PathValue("torrentID"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "Некорректный ID загрузки")
		return
	}
	switch action {
	case "start":
		err = s.transmission.Start(r.Context(), id)
	case "stop":
		err = s.transmission.Stop(r.Context(), id)
	case "remove":
		// Удаляется только задача Transmission. Уже скачанные файлы сохраняются.
		err = s.transmission.Remove(r.Context(), id, false)
	case "remove-data":
		// Это отдельное действие с явным подтверждением в интерфейсе: задача и файлы удаляются.
		err = s.transmission.Remove(r.Context(), id, true)
	default:
		err = errors.New("unknown download action")
	}
	if err != nil {
		s.logger.Error("control Transmission download", "action", action, "id", id, "error", err)
		writeError(w, http.StatusBadGateway, "Не удалось изменить загрузку")
		return
	}
	_ = s.store.Log(r.Context(), "transmission", action, strconv.FormatInt(id, 10), "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) animeCatalog(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(query)) < 2 {
		writeError(w, http.StatusBadRequest, "Поисковый запрос должен содержать минимум 2 символа")
		return
	}
	yearFrom, err := optionalYear(r.URL.Query().Get("year_from"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	yearTo, err := optionalYear(r.URL.Query().Get("year_to"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if yearFrom > 0 && yearTo > 0 && yearFrom > yearTo {
		writeError(w, http.StatusBadRequest, "Начальный год не может быть больше конечного")
		return
	}
	filter := aniliberty.CatalogFilter{
		Search: query, Type: strings.ToUpper(r.URL.Query().Get("type")),
		YearFrom: yearFrom, YearTo: yearTo,
		Sorting:       strings.ToUpper(r.URL.Query().Get("sorting")),
		PublishStatus: strings.ToUpper(r.URL.Query().Get("status")),
	}
	if !allowed(filter.Type, "", "TV", "ONA", "WEB", "OVA", "OAD", "MOVIE", "DORAMA", "SPECIAL") ||
		!allowed(filter.Sorting, "", "FRESH_AT_DESC", "FRESH_AT_ASC", "RATING_DESC", "RATING_ASC", "YEAR_DESC", "YEAR_ASC") ||
		!allowed(filter.PublishStatus, "", "IS_ONGOING", "IS_NOT_ONGOING") {
		writeError(w, http.StatusBadRequest, "Некорректное значение фильтра")
		return
	}
	response, err := s.anime.Catalog(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	_ = s.store.Log(r.Context(), "aniliberty", "catalog-search", "", query)
	writeJSON(w, http.StatusOK, response)
}

func optionalYear(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	year, err := strconv.Atoi(value)
	if err != nil || year < 1960 || year > time.Now().Year()+2 {
		return 0, errors.New("Год должен быть от 1960 до текущего")
	}
	return year, nil
}

func allowed(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}

func (s *Server) animeSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(query)) < 2 {
		writeError(w, http.StatusBadRequest, "Поисковый запрос должен содержать минимум 2 символа")
		return
	}
	items, err := s.anime.Search(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	_ = s.store.Log(r.Context(), "aniliberty", "search", "", query)
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) animeTorrents(w http.ResponseWriter, r *http.Request) {
	releaseID, err := strconv.ParseInt(r.PathValue("releaseID"), 10, 64)
	if err != nil || releaseID <= 0 {
		writeError(w, http.StatusBadRequest, "Некорректный ID релиза")
		return
	}
	items, err := s.anime.Torrents(r.Context(), releaseID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) animeDownload(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ReleaseID int64  `json:"release_id"`
		TorrentID int64  `json:"torrent_id"`
		Title     string `json:"title"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	torrents, err := s.anime.Torrents(r.Context(), input.ReleaseID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	var selected *aniliberty.Torrent
	for i := range torrents {
		if torrents[i].ID == input.TorrentID {
			selected = &torrents[i]
			break
		}
	}
	if selected == nil || selected.Magnet == "" {
		writeError(w, http.StatusNotFound, "Торрент не найден")
		return
	}
	result, err := s.transmission.AddMagnet(r.Context(), selected.Magnet, s.cfg.AnimeDownloadDir)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.trackNewDownload(r.Context(), result)
	s.organizeTorrent(result, input.Title, s.cfg.AnimeDownloadDir)
	_ = s.store.Log(r.Context(), "aniliberty", "download", strconv.FormatInt(selected.ID, 10), input.Title)
	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) trackerSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(query)) < 2 {
		writeError(w, http.StatusBadRequest, "Поисковый запрос должен содержать минимум 2 символа")
		return
	}
	items, err := s.tracker.Search(r.Context(), query)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, rutracker.ErrNotConfigured) {
			status = http.StatusServiceUnavailable
		}
		writeError(w, status, err.Error())
		return
	}
	_ = s.store.Log(r.Context(), "rutracker", "search", "", query)
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) trackerDownload(w http.ResponseWriter, r *http.Request) {
	var input struct {
		TopicID  int64  `json:"topic_id"`
		Title    string `json:"title"`
		Category string `json:"category"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	torrent, err := s.tracker.Download(r.Context(), input.TopicID)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, rutracker.ErrNotConfigured) {
			status = http.StatusServiceUnavailable
		}
		writeError(w, status, err.Error())
		return
	}
	downloadDir := s.cfg.MovieDownloadDir
	if input.Category == "music" {
		downloadDir = s.cfg.MusicDownloadDir
	}
	result, err := s.transmission.AddTorrent(r.Context(), torrent, downloadDir)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.trackNewDownload(r.Context(), result)
	s.organizeTorrent(result, input.Title, downloadDir)
	_ = s.store.Log(r.Context(), "rutracker", "download", strconv.FormatInt(input.TopicID, 10), input.Title)
	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) trackNewDownload(ctx context.Context, result map[string]any) {
	if id, ok := addedTorrentID(result); ok {
		_ = s.store.SetTelegramDownloadState(ctx, id, store.TelegramDownloadState{})
	}
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authenticated(r) {
			next.ServeHTTP(w, r)
			return
		}
		if username, password, ok := r.BasicAuth(); ok && secureEqual(username, s.cfg.APIUsername) && secureEqual(password, s.cfg.APIPassword) {
			s.setSessionCookie(w, true)
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusUnauthorized, "Требуется авторизация")
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}

func (s *Server) mutation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-HomeNode-Request") != "1" {
			writeError(w, http.StatusForbidden, "Отсутствует защитный заголовок")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https://aniliberty.top https://www.aniliberty.top data:; media-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}

func secureEqual(left, right string) bool {
	a := sha256.Sum256([]byte(left))
	b := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "Некорректный JSON")
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSONStatus(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) { writeJSONStatus(w, status, value) }

func writeJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	httpServer := &http.Server{
		Addr: s.cfg.ListenAddress, Handler: s.handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	done := make(chan error, 1)
	go func() { done <- httpServer.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("HTTP server: %w", err)
	}
}
