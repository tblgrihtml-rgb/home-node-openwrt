package server

import (
	"errors"
	"io/fs"
	"math"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"homenode/services/home-api/internal/store"
)

type watchProgressInput struct {
	Path            string  `json:"path"`
	Title           string  `json:"title"`
	PositionSeconds float64 `json:"position_seconds"`
	DurationSeconds float64 `json:"duration_seconds"`
	Completed       bool    `json:"completed"`
}

func (s *Server) watchProgressList(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.store.RecentWatchProgress(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось прочитать историю просмотра")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) watchProgressUpdate(w http.ResponseWriter, r *http.Request) {
	var input watchProgressInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Path = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(input.Path))))
	if len(input.Path) > 2048 || len(input.Title) > 300 || invalidWatchNumber(input.PositionSeconds) || invalidWatchNumber(input.DurationSeconds) {
		writeError(w, http.StatusBadRequest, "Некорректные данные прогресса")
		return
	}
	file, _, err := openMediaFile(s.cfg.MediaRoot, input.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusNotFound, "Медиафайл не найден")
			return
		}
		writeError(w, http.StatusBadRequest, "Некорректный путь к медиафайлу")
		return
	}
	_ = file.Close()

	if input.DurationSeconds > 0 && input.PositionSeconds > input.DurationSeconds {
		input.PositionSeconds = input.DurationSeconds
	}
	if input.DurationSeconds > 0 && input.PositionSeconds/input.DurationSeconds >= 0.95 {
		input.Completed = true
	}
	parts := strings.Split(input.Path, "/")
	category := parts[0]
	progress := store.WatchProgress{
		Path: input.Path, Title: strings.TrimSpace(input.Title), Category: category,
		PositionSeconds: input.PositionSeconds, DurationSeconds: input.DurationSeconds, Completed: input.Completed,
	}
	if err := s.store.SetWatchProgress(r.Context(), progress); err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось сохранить прогресс просмотра")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func invalidWatchNumber(value float64) bool {
	return value < 0 || value > 7*24*60*60 || math.IsNaN(value) || math.IsInf(value, 0)
}
