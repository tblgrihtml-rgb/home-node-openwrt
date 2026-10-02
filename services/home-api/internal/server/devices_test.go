package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"homenode/services/home-api/internal/config"
)

func TestDevicesIncludesStaticLANNeighbors(t *testing.T) {
	directory := t.TempDir()
	leasesPath := filepath.Join(directory, "dhcp.leases")
	arpPath := filepath.Join(directory, "arp")
	fullPath := filepath.Join(directory, "vpn-devices")
	if err := os.WriteFile(leasesPath, []byte("123 02:00:00:00:00:01 192.0.2.10 laptop *\n"), 0600); err != nil {
		t.Fatal(err)
	}
	arp := "IP address HW type Flags HW address Mask Device\n" +
		"192.0.2.10 0x1 0x2 02:00:00:00:00:01 * br-lan\n" +
		"192.0.2.185 0x1 0x2 02:00:00:00:00:02 * br-lan\n" +
		"198.51.100.1 0x1 0x2 02:00:00:00:00:03 * wan\n" +
		"192.0.2.99 0x1 0x0 02:00:00:00:00:04 * br-lan\n"
	if err := os.WriteFile(arpPath, []byte(arp), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte("02:00:00:00:00:02\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: config.Config{
		VPNDevicesPath: fullPath, VPNSmartDevicesPath: filepath.Join(directory, "vpn-smart-devices"),
		DHCPLeasesPath: leasesPath, ARPPath: arpPath,
	}}
	response := httptest.NewRecorder()
	s.devices(response, httptest.NewRequest(http.MethodGet, "/api/devices", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("devices failed: %d %s", response.Code, response.Body.String())
	}
	var devices []Device
	if err := json.Unmarshal(response.Body.Bytes(), &devices); err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 {
		t.Fatalf("expected one DHCP and one static LAN device, got %#v", devices)
	}
	byMAC := map[string]Device{}
	for _, device := range devices {
		byMAC[device.MAC] = device
	}
	if got := byMAC["02:00:00:00:00:01"]; got.Name != "laptop" || !got.Online {
		t.Fatalf("DHCP device was lost or duplicated: %#v", got)
	}
	if got := byMAC["02:00:00:00:00:02"]; got.IP != "192.0.2.185" || !got.Online || got.VPNMode != vpnModeFull {
		t.Fatalf("static VPN device was not discovered: %#v", got)
	}
}

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
