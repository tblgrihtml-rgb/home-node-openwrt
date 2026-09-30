package config

import (
	"errors"
	"net"
	"os"
	"strings"
	"time"
)

type Config struct {
	ListenAddress        string
	DatabasePath         string
	BackupDir            string
	SystemHealthPath     string
	CompatibleCacheDir   string
	MediaRoot            string
	AnimeDownloadDir     string
	MovieDownloadDir     string
	MusicDownloadDir     string
	APIUsername          string
	APIPassword          string
	SessionSecret        string
	AniLibertyBaseURL    string
	TransmissionRPCURL   string
	TransmissionUsername string
	TransmissionPassword string
	RuTrackerBaseURL     string
	RuTrackerProxyURL    string
	RuTrackerUsername    string
	RuTrackerPassword    string
	TelegramBotToken     string
	TelegramAPIBaseURL   string
	TelegramAppURL       string
	HomeNodeInternalURL  string
	FFmpegPath           string
	VPNBindAddress       string
	VPNDevicesPath       string
	VPNSmartDevicesPath  string
	DHCPLeasesPath       string
	ARPPath              string
	RequestTimeout       time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		ListenAddress:        env("HOMENODE_LISTEN", "0.0.0.0:8787"),
		DatabasePath:         env("HOMENODE_DB", "/mnt/ssd/homenode/data/homenode.db"),
		BackupDir:            env("HOMENODE_BACKUP_DIR", "/mnt/ssd/homenode/data/backups"),
		SystemHealthPath:     env("HOMENODE_SYSTEM_HEALTH", "/mnt/ssd/homenode/data/system-health.json"),
		CompatibleCacheDir:   env("HOMENODE_COMPATIBLE_CACHE", "/mnt/ssd/homenode/data/compatible-cache"),
		MediaRoot:            env("HOMENODE_MEDIA_ROOT", "/mnt/ssd/media"),
		AnimeDownloadDir:     env("HOMENODE_ANIME_DIR", "/mnt/ssd/media/anime"),
		MovieDownloadDir:     env("HOMENODE_MOVIES_DIR", "/mnt/ssd/media/movies"),
		MusicDownloadDir:     env("HOMENODE_MUSIC_DIR", "/mnt/ssd/media/music"),
		APIUsername:          strings.TrimSpace(os.Getenv("HOMENODE_USER")),
		APIPassword:          os.Getenv("HOMENODE_PASSWORD"),
		SessionSecret:        os.Getenv("HOMENODE_SESSION_SECRET"),
		AniLibertyBaseURL:    strings.TrimRight(env("ANILIBERTY_BASE_URL", "https://aniliberty.top/api/v1"), "/"),
		TransmissionRPCURL:   env("TRANSMISSION_RPC_URL", "http://192.168.1.1:9091/transmission/rpc"),
		TransmissionUsername: os.Getenv("TRANSMISSION_RPC_USERNAME"),
		TransmissionPassword: os.Getenv("TRANSMISSION_RPC_PASSWORD"),
		RuTrackerBaseURL:     strings.TrimRight(env("RUTRACKER_BASE_URL", "https://rutracker.org"), "/"),
		RuTrackerProxyURL:    strings.TrimSpace(os.Getenv("RUTRACKER_PROXY_URL")),
		RuTrackerUsername:    strings.TrimSpace(os.Getenv("RUTRACKER_USERNAME")),
		RuTrackerPassword:    os.Getenv("RUTRACKER_PASSWORD"),
		TelegramBotToken:     strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
		TelegramAPIBaseURL:   strings.TrimRight(env("TELEGRAM_API_BASE_URL", "https://api.telegram.org"), "/"),
		TelegramAppURL:       env("TELEGRAM_APP_URL", "http://192.168.1.1:8787/"),
		HomeNodeInternalURL:  strings.TrimRight(env("HOMENODE_INTERNAL_URL", "http://192.168.1.1:8787"), "/"),
		FFmpegPath:           env("FFMPEG_PATH", "/usr/bin/ffmpeg"),
		VPNBindAddress:       env("VPN_BIND_ADDRESS", ""),
		VPNDevicesPath:       env("VPN_DEVICES_PATH", "/mnt/ssd/homenode/data/vpn-devices"),
		VPNSmartDevicesPath:  env("VPN_SMART_DEVICES_PATH", "/mnt/ssd/homenode/data/vpn-smart-devices"),
		DHCPLeasesPath:       env("DHCP_LEASES_PATH", "/tmp/dhcp.leases"),
		ARPPath:              env("ARP_PATH", "/proc/net/arp"),
		RequestTimeout:       15 * time.Second,
	}

	if cfg.APIUsername == "" || cfg.APIPassword == "" {
		return Config{}, errors.New("HOMENODE_USER and HOMENODE_PASSWORD are required")
	}
	if len(cfg.SessionSecret) < 32 {
		return Config{}, errors.New("HOMENODE_SESSION_SECRET must contain at least 32 characters")
	}
	if cfg.TransmissionUsername == "" || cfg.TransmissionPassword == "" {
		return Config{}, errors.New("Transmission RPC credentials are required")
	}
	if cfg.VPNBindAddress != "" && net.ParseIP(cfg.VPNBindAddress) == nil {
		return Config{}, errors.New("VPN_BIND_ADDRESS must be a valid IP address")
	}
	return cfg, nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
