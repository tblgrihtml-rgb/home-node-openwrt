package server

import "net/http"

func (s *Server) telegramStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.telegram.Status(r.Context()))
}

func (s *Server) telegramPairing(w http.ResponseWriter, r *http.Request) {
	pairing, err := s.telegram.CreatePairingCode(r.Context())
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, pairing)
}

func (s *Server) telegramUnpair(w http.ResponseWriter, r *http.Request) {
	if err := s.telegram.Unpair(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось отключить Telegram")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) telegramTest(w http.ResponseWriter, r *http.Request) {
	if err := s.telegram.SendTestMessage(r.Context()); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}
