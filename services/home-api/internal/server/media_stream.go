package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var compatibleCacheMu sync.Mutex

var mediaContentTypes = map[string]string{
	".aac":  "audio/aac",
	".avi":  "video/x-msvideo",
	".flac": "audio/flac",
	".m4a":  "audio/mp4",
	".m4v":  "video/x-m4v",
	".mkv":  "video/x-matroska",
	".mp3":  "audio/mpeg",
	".mp4":  "video/mp4",
	".ogg":  "audio/ogg",
	".opus": "audio/ogg",
	".wav":  "audio/wav",
}

var videoExtensions = map[string]bool{
	".avi": true, ".m4v": true, ".mkv": true, ".mp4": true,
}

func isSupportedMediaExtension(ext string) bool {
	_, ok := mediaContentTypes[strings.ToLower(ext)]
	return ok
}

func isVideoExtension(ext string) bool {
	return videoExtensions[strings.ToLower(ext)]
}

func (s *Server) mediaStream(w http.ResponseWriter, r *http.Request) {
	disableWriteDeadline(w)
	file, info, err := openMediaFile(s.cfg.MediaRoot, r.URL.Query().Get("path"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusNotFound, "Медиафайл не найден")
			return
		}
		writeError(w, http.StatusBadRequest, "Некорректный путь к медиафайлу")
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(info.Name()))
	w.Header().Set("Content-Type", mediaContentTypes[ext])
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

// mediaCompatible repackages the original video into a fragmented MP4 stream.
// The video is copied without re-encoding, so this is light enough for the router;
// subtitles are intentionally omitted because Safari cannot render ASS from MP4.
func (s *Server) mediaCompatible(w http.ResponseWriter, r *http.Request) {
	disableWriteDeadline(w)
	file, info, err := openMediaFile(s.cfg.MediaRoot, r.URL.Query().Get("path"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusNotFound, "Видео не найдено")
			return
		}
		writeError(w, http.StatusBadRequest, "Некорректный путь к видео")
		return
	}
	if !isVideoExtension(filepath.Ext(info.Name())) {
		_ = file.Close()
		writeError(w, http.StatusBadRequest, "Совместимый режим предназначен только для видео")
		return
	}
	path := file.Name()
	_ = file.Close()
	if r.URL.Query().Get("cache") == "1" {
		s.mediaCompatibleCached(w, r, path, info)
		return
	}

	if _, err := exec.LookPath(s.cfg.FFmpegPath); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Режим Safari недоступен: ffmpeg не установлен")
		return
	}

	args := []string{"-v", "error", "-nostdin"}
	start, err := strconv.ParseFloat(r.URL.Query().Get("start"), 64)
	if err == nil && start > 0 && start <= 7*24*60*60 {
		args = append(args, "-ss", strconv.FormatFloat(start, 'f', 3, 64))
	}
	args = append(args,
		"-i", path,
		"-map", "0:v:0", "-map", "0:a:0?",
		"-c", "copy", "-sn", "-dn",
	)
	if isHEVC(r.Context(), s.cfg.FFmpegPath, path) {
		args = append(args, "-tag:v", "hvc1")
	}
	args = append(args,
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"-f", "mp4", "pipe:1",
	)
	cmd := exec.CommandContext(r.Context(), s.cfg.FFmpegPath, args...)
	if s.logger != nil {
		s.logger.Info("Safari stream started", "file", filepath.Base(path), "transport", "fragmented")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось подготовить видео")
		return
	}
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Не удалось запустить режим Safari")
		return
	}

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Duration", "stream")
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	_, copyErr := io.Copy(w, stdout)
	waitErr := cmd.Wait()
	if copyErr != nil && r.Context().Err() == nil && s.logger != nil {
		s.logger.Warn("Safari stream interrupted", "file", filepath.Base(path), "error", copyErr)
	}
	if waitErr != nil && r.Context().Err() == nil && s.logger != nil {
		s.logger.Warn("Safari remux failed", "file", filepath.Base(path), "error", waitErr, "ffmpeg", stderr.String())
	}
}

// mediaCompatibleCached creates a regular seekable MP4 for Safari over remote
// Tailscale links. A normal file with Content-Length and Range support is more
// resilient than a never-ending fragmented response through reverse proxies.
func (s *Server) mediaCompatibleCached(w http.ResponseWriter, r *http.Request, sourcePath string, sourceInfo fs.FileInfo) {
	if err := os.MkdirAll(s.cfg.CompatibleCacheDir, 0750); err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось подготовить кэш видео")
		return
	}
	identity := sourcePath + "\x00" + strconv.FormatInt(sourceInfo.Size(), 10) + "\x00" + strconv.FormatInt(sourceInfo.ModTime().UnixNano(), 10)
	sum := sha256.Sum256([]byte(identity))
	cachePath := filepath.Join(s.cfg.CompatibleCacheDir, hex.EncodeToString(sum[:])+".mp4")

	compatibleCacheMu.Lock()
	if _, err := os.Stat(cachePath); errors.Is(err, os.ErrNotExist) {
		temporary, err := os.CreateTemp(s.cfg.CompatibleCacheDir, ".compatible-*.mp4")
		if err != nil {
			compatibleCacheMu.Unlock()
			writeError(w, http.StatusInternalServerError, "Не удалось подготовить кэш видео")
			return
		}
		temporaryPath := temporary.Name()
		_ = temporary.Close()
		defer os.Remove(temporaryPath)
		args := []string{"-v", "error", "-nostdin", "-i", sourcePath, "-map", "0:v:0", "-map", "0:a:0?", "-c", "copy", "-sn", "-dn"}
		if isHEVC(r.Context(), s.cfg.FFmpegPath, sourcePath) {
			args = append(args, "-tag:v", "hvc1")
		}
		args = append(args, "-movflags", "+faststart", "-y", temporaryPath)
		cmd := exec.CommandContext(r.Context(), s.cfg.FFmpegPath, args...)
		var stderr limitedBuffer
		cmd.Stderr = &stderr
		started := time.Now()
		if s.logger != nil {
			s.logger.Info("Safari cache remux started", "file", filepath.Base(sourcePath))
		}
		if err := cmd.Run(); err != nil {
			compatibleCacheMu.Unlock()
			if s.logger != nil {
				s.logger.Warn("Safari cache remux failed", "file", filepath.Base(sourcePath), "error", err, "ffmpeg", stderr.String())
			}
			writeError(w, http.StatusServiceUnavailable, "Не удалось подготовить видео для Safari")
			return
		}
		if err := os.Rename(temporaryPath, cachePath); err != nil {
			compatibleCacheMu.Unlock()
			writeError(w, http.StatusInternalServerError, "Не удалось сохранить совместимое видео")
			return
		}
		if s.logger != nil {
			s.logger.Info("Safari cache remux ready", "file", filepath.Base(sourcePath), "elapsed", time.Since(started).Round(time.Millisecond))
		}
		cleanupCompatibleCache(s.cfg.CompatibleCacheDir)
	}
	compatibleCacheMu.Unlock()

	file, err := os.Open(cachePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Совместимое видео недоступно")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Совместимое видео недоступно")
		return
	}
	_ = os.Chtimes(cachePath, time.Now(), time.Now())
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, sourceInfo.Name()+".mp4", info.ModTime(), file)
}

func cleanupCompatibleCache(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type cachedFile struct {
		path string
		size int64
		mod  time.Time
	}
	files := make([]cachedFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".mp4" {
			continue
		}
		info, err := entry.Info()
		if err == nil {
			files = append(files, cachedFile{path: filepath.Join(dir, entry.Name()), size: info.Size(), mod: info.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	var total int64
	for index, file := range files {
		total += file.size
		if index >= 4 || total > 4<<30 {
			_ = os.Remove(file.path)
		}
	}
}

func disableWriteDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
}

func isHEVC(parent context.Context, ffmpegPath, mediaPath string) bool {
	probePath := filepath.Join(filepath.Dir(ffmpegPath), "ffprobe")
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	// Some OpenWrt ffprobe builds try an unavailable hardware decoder and exit
	// non-zero, but still print reliable stream metadata.
	output, _ := exec.CommandContext(ctx, probePath, "-hide_banner", mediaPath).CombinedOutput()
	return bytes.Contains(output, []byte("Video: hevc"))
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	const limit = 4096
	original := len(p)
	if b.Len() < limit {
		remaining := limit - b.Len()
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return original, nil
}

func openMediaFile(root, requested string) (*os.File, fs.FileInfo, error) {
	if requested == "" {
		return nil, nil, fs.ErrInvalid
	}
	clean := filepath.Clean(filepath.FromSlash(requested))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, nil, fs.ErrInvalid
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, nil, err
	}
	resolvedPath, err := filepath.EvalSymlinks(filepath.Join(resolvedRoot, clean))
	if err != nil {
		return nil, nil, err
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, nil, fs.ErrInvalid
	}
	if !isSupportedMediaExtension(filepath.Ext(resolvedPath)) {
		return nil, nil, fs.ErrInvalid
	}

	file, err := os.Open(resolvedPath)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, fs.ErrInvalid
	}
	return file, info, nil
}
