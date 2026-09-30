package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Device struct {
	MAC     string `json:"mac"`
	IP      string `json:"ip"`
	Name    string `json:"name"`
	Online  bool   `json:"online"`
	VPN     bool   `json:"vpn"`
	VPNMode string `json:"vpn_mode"`
}

const (
	vpnModeDirect = "direct"
	vpnModeSmart  = "smart"
	vpnModeFull   = "full"
)

func (s *Server) devices(w http.ResponseWriter, _ *http.Request) {
	full, err := readMACSet(s.cfg.VPNDevicesPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось прочитать настройки VPN")
		return
	}
	smart, err := readMACSet(s.cfg.VPNSmartDevicesPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось прочитать настройки выборочного VPN")
		return
	}
	online := readARP(s.cfg.ARPPath)
	data, err := os.ReadFile(s.cfg.DHCPLeasesPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusInternalServerError, "Не удалось прочитать список устройств")
		return
	}
	devices := make([]Device, 0)
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		mac, ok := normalizeMAC(fields[1])
		if !ok || net.ParseIP(fields[2]) == nil {
			continue
		}
		name := fields[3]
		if name == "*" || name == "" {
			name = "Устройство " + strings.ToUpper(strings.ReplaceAll(mac[len(mac)-5:], ":", ""))
		}
		mode := deviceVPNMode(mac, smart, full)
		devices = append(devices, Device{MAC: mac, IP: fields[2], Name: name, Online: online[mac], VPN: mode != vpnModeDirect, VPNMode: mode})
		seen[mac] = true
	}
	selected := make(map[string]bool, len(smart)+len(full))
	for mac := range smart {
		selected[mac] = true
	}
	for mac := range full {
		selected[mac] = true
	}
	for mac := range selected {
		if !seen[mac] {
			mode := deviceVPNMode(mac, smart, full)
			devices = append(devices, Device{MAC: mac, Name: "Сохранённое устройство", VPN: true, VPNMode: mode})
		}
	}
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].Online != devices[j].Online {
			return devices[i].Online
		}
		return strings.ToLower(devices[i].Name) < strings.ToLower(devices[j].Name)
	})
	writeJSON(w, http.StatusOK, devices)
}

func (s *Server) deviceVPN(w http.ResponseWriter, r *http.Request) {
	mac, ok := normalizeMAC(r.PathValue("mac"))
	if !ok {
		writeError(w, http.StatusBadRequest, "Некорректный MAC-адрес")
		return
	}
	full, err := readMACSet(s.cfg.VPNDevicesPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось прочитать настройки VPN")
		return
	}
	smart, err := readMACSet(s.cfg.VPNSmartDevicesPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось прочитать настройки выборочного VPN")
		return
	}
	mode := vpnModeDirect
	if r.Method == http.MethodPost {
		mode = vpnModeFull
		var input struct {
			Mode string `json:"mode"`
		}
		if r.Body != nil && r.ContentLength != 0 {
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				writeError(w, http.StatusBadRequest, "Некорректный режим VPN")
				return
			}
			mode = strings.ToLower(strings.TrimSpace(input.Mode))
		}
		if mode != vpnModeSmart && mode != vpnModeFull {
			writeError(w, http.StatusBadRequest, "Режим VPN должен быть smart или full")
			return
		}
	}
	delete(smart, mac)
	delete(full, mac)
	if mode == vpnModeSmart {
		smart[mac] = true
	} else if mode == vpnModeFull {
		full[mac] = true
	}
	if err := writeMACSet(s.cfg.VPNSmartDevicesPath, smart); err != nil {
		s.logger.Error("save smart VPN devices", "error", err)
		writeError(w, http.StatusInternalServerError, "Не удалось сохранить настройки выборочного VPN")
		return
	}
	if err := writeMACSet(s.cfg.VPNDevicesPath, full); err != nil {
		s.logger.Error("save VPN devices", "error", err)
		writeError(w, http.StatusInternalServerError, "Не удалось сохранить настройки VPN")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mac": mac, "vpn": mode != vpnModeDirect, "vpn_mode": mode})
}

func deviceVPNMode(mac string, smart, full map[string]bool) string {
	if full[mac] {
		return vpnModeFull
	}
	if smart[mac] {
		return vpnModeSmart
	}
	return vpnModeDirect
}

func normalizeMAC(value string) (string, bool) {
	hardware, err := net.ParseMAC(strings.TrimSpace(value))
	if err != nil || len(hardware) != 6 {
		return "", false
	}
	return strings.ToLower(hardware.String()), true
}

func readMACSet(path string) (map[string]bool, error) {
	result := make(map[string]bool)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if mac, ok := normalizeMAC(line); ok {
			result[mac] = true
		}
	}
	return result, nil
}

func writeMACSet(path string, selected map[string]bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	values := make([]string, 0, len(selected))
	for mac := range selected {
		values = append(values, mac)
	}
	sort.Strings(values)
	temporary, err := os.CreateTemp(filepath.Dir(path), ".vpn-devices-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0640); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(strings.Join(values, "\n") + "\n"); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func readARP(path string) map[string]bool {
	result := make(map[string]bool)
	data, err := os.ReadFile(path)
	if err != nil {
		return result
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[2] == "0x2" {
			if mac, ok := normalizeMAC(fields[3]); ok {
				result[mac] = true
			}
		}
	}
	return result
}
