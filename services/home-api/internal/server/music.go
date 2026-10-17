package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"homenode/services/home-api/internal/music"
)

func (s *Server) RunMusicQueue(ctx context.Context) {
	music.Run(ctx, s.store, s.music, s.cfg.MusicDownloadDir, s.logger)
}

func (s *Server) musicStatus(w http.ResponseWriter, r *http.Request) {
	ok, expires := s.music.Verified()
	cooldown, err := s.store.MusicCooldown(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось прочитать состояние источника музыки")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"verified": ok, "expires_at": expires, "cooldown_until": cooldown.UTC().Format(time.RFC3339), "provider": "SpotiFLAC Community · Tidal"})
}

func (s *Server) musicSearch(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	tracks, err := s.music.Search(ctx, r.URL.Query().Get("q"))
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tracks": tracks})
}

func (s *Server) musicJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.store.ListMusicJobs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось прочитать очередь музыки")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (s *Server) musicJobAdd(w http.ResponseWriter, r *http.Request) {
	var input struct {
		TrackID int64 `json:"track_id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.TrackID <= 0 {
		writeError(w, http.StatusBadRequest, "Неверный ID трека")
		return
	}
	jobs, err := s.store.ListMusicJobs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось проверить очередь")
		return
	}
	if len(jobs) >= 200 {
		writeError(w, http.StatusConflict, "Очередь заполнена: удалите старые записи")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	track, err := s.music.Lookup(ctx, input.TrackID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	job, err := s.store.AddMusicJob(r.Context(), track.ID, track.Title, track.Artist, track.Album)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось добавить трек")
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func musicJobID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid job ID")
	}
	return id, nil
}

func (s *Server) musicJobRetry(w http.ResponseWriter, r *http.Request) {
	id, err := musicJobID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Неверный номер задания")
		return
	}
	ok, err := s.store.RetryMusicJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось повторить загрузку")
		return
	}
	if !ok {
		writeError(w, http.StatusConflict, "Эту загрузку нельзя повторить")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) musicJobRemove(w http.ResponseWriter, r *http.Request) {
	id, err := musicJobID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Неверный номер задания")
		return
	}
	ok, err := s.store.RemoveMusicJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось убрать запись из очереди")
		return
	}
	if !ok {
		writeError(w, http.StatusConflict, "Дождитесь окончания текущей загрузки")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) musicVerifyStart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	challenge, err := s.music.StartVerification(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "Не удалось начать проверку источника музыки")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": challenge})
}

func (s *Server) musicVerifyComplete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var input struct {
		CallbackURL string `json:"callback_url"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "Вставьте полный адрес из браузера")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	err := s.music.CompleteVerificationURL(ctx, input.CallbackURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Проверка не завершена: адрес неверный или устарел")
		return
	}
	_ = s.store.ResumeMusicVerificationJobs(r.Context())
	writeJSON(w, http.StatusOK, map[string]bool{"verified": true})
}
