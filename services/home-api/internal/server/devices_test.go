package server

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"homenode/services/home-api/internal/config"
)

func TestMACSetRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vpn-devices")
	input := map[string]bool{"aa:bb:cc:dd:ee:ff": true, "02:00:00:00:00:01": true}
	if err := writeMACSet(path, input); err != nil {
		t.Fatal(err)
	}
	result, err := readMACSet(path)
	if err != nil || len(result) != 2 || !result["aa:bb:cc:dd:ee:ff"] {
		t.Fatalf("unexpected set: %#v, err=%v", result, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("unexpected mode: %v, err=%v", info.Mode().Perm(), err)
	}
}

func TestNormalizeMACRejectsInvalidValues(t *testing.T) {
	if value, ok := normalizeMAC("AA-BB-CC-DD-EE-FF"); !ok || value != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("unexpected normalization: %q, %v", value, ok)
	}
	if _, ok := normalizeMAC("../../etc/passwd"); ok {
		t.Fatal("invalid MAC must be rejected")
	}
}

func TestDeviceVPNMode(t *testing.T) {
	mac := "aa:bb:cc:dd:ee:ff"
	if got := deviceVPNMode(mac, nil, nil); got != vpnModeDirect {
		t.Fatalf("unexpected direct mode: %q", got)
	}
	if got := deviceVPNMode(mac, map[string]bool{mac: true}, nil); got != vpnModeSmart {
		t.Fatalf("unexpected smart mode: %q", got)
	}
	if got := deviceVPNMode(mac, map[string]bool{mac: true}, map[string]bool{mac: true}); got != vpnModeFull {
		t.Fatalf("full mode must win for a stale duplicate: %q", got)
	}
}

func TestDeviceVPNChangesExclusiveModes(t *testing.T) {
	directory := t.TempDir()
	fullPath := filepath.Join(directory, "vpn-devices")
	smartPath := filepath.Join(directory, "vpn-smart-devices")
	s := &Server{
		cfg:    config.Config{VPNDevicesPath: fullPath, VPNSmartDevicesPath: smartPath},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	mac := "aa:bb:cc:dd:ee:ff"

	request := httptest.NewRequest(http.MethodPost, "/api/devices/"+mac+"/vpn", bytes.NewBufferString(`{"mode":"smart"}`))
	request.SetPathValue("mac", mac)
	response := httptest.NewRecorder()
	s.deviceVPN(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("smart mode failed: %d %s", response.Code, response.Body.String())
	}
	smart, _ := readMACSet(smartPath)
	full, _ := readMACSet(fullPath)
	if !smart[mac] || full[mac] {
		t.Fatalf("unexpected smart/full sets: %#v %#v", smart, full)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/devices/"+mac+"/vpn", bytes.NewBufferString(`{"mode":"full"}`))
	request.SetPathValue("mac", mac)
	response = httptest.NewRecorder()
	s.deviceVPN(response, request)
	smart, _ = readMACSet(smartPath)
	full, _ = readMACSet(fullPath)
	if smart[mac] || !full[mac] {
		t.Fatalf("unexpected smart/full sets after full: %#v %#v", smart, full)
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/devices/"+mac+"/vpn", nil)
	request.SetPathValue("mac", mac)
	response = httptest.NewRecorder()
	s.deviceVPN(response, request)
	smart, _ = readMACSet(smartPath)
	full, _ = readMACSet(fullPath)
	if smart[mac] || full[mac] {
		t.Fatalf("device must be direct after delete: %#v %#v", smart, full)
	}
}
