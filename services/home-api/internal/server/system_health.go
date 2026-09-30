package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type systemHealthSnapshot struct {
	UpdatedAt          string  `json:"updated_at"`
	VPNStatus          string  `json:"vpn_status"`
	VPNLatencyMS       float64 `json:"vpn_latency_ms"`
	VPNPacketLoss      float64 `json:"vpn_packet_loss"`
	VPNHandshakeAgeSec int64   `json:"vpn_handshake_age_seconds"`
	VPNRXBytes         int64   `json:"vpn_rx_bytes"`
	VPNTXBytes         int64   `json:"vpn_tx_bytes"`
	SSDStatus          string  `json:"ssd_status"`
	SSDTemperature     int64   `json:"ssd_temperature_c"`
	SSDReallocated     int64   `json:"ssd_reallocated_sectors"`
	SSDCRCErrorCount   int64   `json:"ssd_crc_errors"`
	SSDPowerOnHours    int64   `json:"ssd_power_on_hours"`
}

func (s *Server) systemHealth(w http.ResponseWriter, r *http.Request) {
	result := systemHealthSnapshot{VPNStatus: "unknown", SSDStatus: "unknown"}
	data, err := os.ReadFile(s.cfg.SystemHealthPath)
	if err == nil {
		if err := json.Unmarshal(data, &result); err != nil {
			s.logger.Warn("decode system health", "error", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		s.logger.Warn("read system health", "error", err)
	}

	payload := map[string]any{
		"updated_at": result.UpdatedAt,
		"vpn": map[string]any{
			"status": result.VPNStatus, "latency_ms": result.VPNLatencyMS,
			"packet_loss": result.VPNPacketLoss, "handshake_age_seconds": result.VPNHandshakeAgeSec,
			"rx_bytes": result.VPNRXBytes, "tx_bytes": result.VPNTXBytes,
		},
		"ssd": map[string]any{
			"status": result.SSDStatus, "temperature_c": result.SSDTemperature,
			"reallocated_sectors": result.SSDReallocated, "crc_errors": result.SSDCRCErrorCount,
			"power_on_hours": result.SSDPowerOnHours,
		},
		"backup": latestBackup(s.cfg.BackupDir),
	}
	if check, err := s.store.QuickCheck(r.Context()); err == nil {
		payload["database_integrity"] = check
	} else {
		payload["database_integrity"] = "error"
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(s.cfg.MediaRoot, &stat); err == nil {
		payload["disk_total"] = int64(stat.Blocks) * int64(stat.Bsize)
		payload["disk_free"] = int64(stat.Bavail) * int64(stat.Bsize)
	}
	writeJSON(w, http.StatusOK, payload)
}

func latestBackup(dir string) map[string]any {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return map[string]any{"status": "missing"}
	}
	type candidate struct {
		name string
		size int64
		mod  time.Time
	}
	items := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "homenode-") || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		info, err := entry.Info()
		if err == nil {
			items = append(items, candidate{name: filepath.Base(entry.Name()), size: info.Size(), mod: info.ModTime()})
		}
	}
	if len(items) == 0 {
		return map[string]any{"status": "missing"}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })
	return map[string]any{"status": "ok", "name": items[0].name, "size": items[0].size, "created_at": items[0].mod.UTC().Format(time.RFC3339)}
}
