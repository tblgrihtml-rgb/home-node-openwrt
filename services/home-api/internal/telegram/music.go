package telegram

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"homenode/services/home-api/internal/store"
)

var musicExtensions = map[string]bool{
	".mp3": true, ".m4a": true, ".aac": true, ".flac": true,
	".ogg": true, ".opus": true, ".wav": true,
}

func musicAttachment(message *Message) (fileID, uniqueID, name, kind string, size int64, performer, title string, ok bool) {
	if message.Audio != nil {
		a := message.Audio
		name = a.FileName
		if name == "" {
			name = a.Title + ".mp3"
		}
		return a.FileID, a.FileUniqueID, name, "audio", a.FileSize, a.Performer, a.Title, true
	}
	if message.Document != nil && musicExtensions[strings.ToLower(filepath.Ext(message.Document.FileName))] {
		d := message.Document
		return d.FileID, d.FileUniqueID, d.FileName, "document", d.FileSize, "", "", true
	}
	return "", "", "", "", 0, "", "", false
}

func safeMusicPart(value, fallback string) string {
	value = strings.TrimSpace(value)
	var out []rune
	for _, char := range value {
		if unicode.IsControl(char) || strings.ContainsRune(`/\\:<>|?*`, char) {
			out = append(out, ' ')
		} else {
			out = append(out, char)
		}
		if len(out) >= 80 {
			break
		}
	}
	clean := strings.Trim(strings.Join(strings.Fields(string(out)), " "), " .")
	if clean == "" || clean == "." || clean == ".." {
		return fallback
	}
	return clean
}

func audioTags(ctx context.Context, path string) map[string]string {
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, probe, "-v", "error", "-show_entries", "format_tags=artist,album,title,track", "-of", "json", path).Output()
	if err != nil {
		return nil
	}
	var result struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}
	if json.Unmarshal(output, &result) != nil {
		return nil
	}
	tags := make(map[string]string)
	for key, value := range result.Format.Tags {
		tags[strings.ToLower(key)] = value
	}
	return tags
}

func sameFile(a, b string) (bool, error) {
	first, err := os.Open(a)
	if err != nil {
		return false, err
	}
	defer first.Close()
	second, err := os.Open(b)
	if err != nil {
		return false, err
	}
	defer second.Close()
	h1, h2 := sha256.New(), sha256.New()
	if _, err := io.Copy(h1, first); err != nil {
		return false, err
	}
	if _, err := io.Copy(h2, second); err != nil {
		return false, err
	}
	return bytes.Equal(h1.Sum(nil), h2.Sum(nil)), nil
}

func (b *Bot) importMusic(ctx context.Context, message *Message) error {
	fileID, uniqueID, name, kind, size, performer, suppliedTitle, ok := musicAttachment(message)
	if !ok {
		return errors.New("поддерживаются MP3, M4A, AAC, FLAC, OGG, OPUS и WAV")
	}
	if fileID == "" || uniqueID == "" {
		return errors.New("Telegram не передал идентификатор файла")
	}
	if size > maxMusicDownload {
		return errors.New("Bot API скачивает файлы только до 20 МБ; большой файл перенесите через SMB")
	}
	if existing, found, err := b.store.TelegramMusicByUniqueID(ctx, uniqueID); err != nil {
		return err
	} else if found {
		_ = b.api.sendMessage(ctx, message.Chat.ID, "Уже добавлено: <b>"+html.EscapeString(existing.Title)+"</b>", musicKeyboard())
		return nil
	}
	ext := strings.ToLower(filepath.Ext(name))
	if !musicExtensions[ext] {
		return errors.New("неподдерживаемый формат аудио")
	}
	if err := os.MkdirAll(b.cfg.MusicDir, 0750); err != nil {
		return errors.New("не удалось открыть папку музыки")
	}
	temp, err := os.CreateTemp(b.cfg.MusicDir, ".telegram-import-*")
	if err != nil {
		return errors.New("не удалось создать временный файл на SSD")
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	written, err := b.api.downloadFile(ctx, fileID, temp)
	closeErr := temp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	tags := audioTags(ctx, tempPath)
	artist := safeMusicPart(performer, "Неизвестный исполнитель")
	if artist == "Неизвестный исполнитель" {
		artist = safeMusicPart(tags["artist"], artist)
	}
	album := safeMusicPart(tags["album"], "Без альбома")
	title := safeMusicPart(suppliedTitle, "")
	if title == "" {
		title = safeMusicPart(tags["title"], "")
	}
	if title == "" {
		title = safeMusicPart(strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)), "Без названия")
	}
	fileBase := safeMusicPart(strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)), title) + ext
	dir := filepath.Join(b.cfg.MusicDir, artist, album)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return errors.New("не удалось создать папку альбома")
	}
	rootReal, err := filepath.EvalSymlinks(b.cfg.MusicDir)
	if err != nil {
		return err
	}
	dirReal, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	if relative, err := filepath.Rel(rootReal, dirReal); err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return errors.New("папка альбома выходит за пределы медиатеки")
	}
	finalPath := filepath.Join(dir, fileBase)
	created := false
	for suffix := 2; ; suffix++ {
		info, statErr := os.Lstat(finalPath)
		if errors.Is(statErr, os.ErrNotExist) {
			break
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode().IsRegular() && info.Size() == written {
			identical, err := sameFile(tempPath, finalPath)
			if err == nil && identical {
				break
			}
		}
		finalPath = filepath.Join(dir, safeMusicPart(strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)), title)+fmt.Sprintf(" (%d)", suffix)+ext)
	}
	if _, err := os.Lstat(finalPath); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(tempPath, finalPath); err != nil {
			return err
		}
		created = true
		if err := os.Chmod(finalPath, 0644); err != nil {
			_ = os.Remove(finalPath)
			return err
		}
	} else if err != nil {
		return err
	}
	relative, err := filepath.Rel(b.cfg.MusicDir, finalPath)
	if err != nil {
		if created {
			_ = os.Remove(finalPath)
		}
		return err
	}
	item := store.TelegramMusicTrack{FileUniqueID: uniqueID, FileID: fileID, FileKind: kind, Path: filepath.ToSlash(relative), Title: title, Artist: artist, Album: album, Size: written}
	if err := b.store.UpsertTelegramMusicTrack(ctx, item); err != nil {
		if created {
			_ = os.Remove(finalPath)
		}
		return err
	}
	_ = b.api.sendMessage(ctx, message.Chat.ID, "✅ Добавлено: <b>"+html.EscapeString(title)+"</b>\n"+html.EscapeString(artist)+" · "+html.EscapeString(album), musicKeyboard())
	return nil
}

func (b *Bot) musicPath(relative string) (string, error) {
	if filepath.IsAbs(relative) || relative == "" {
		return "", errors.New("некорректный путь к музыке")
	}
	root, err := filepath.EvalSymlinks(b.cfg.MusicDir)
	if err != nil {
		return "", err
	}
	path, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", errors.New("файл вне медиатеки")
	}
	return path, nil
}

func (b *Bot) showMusic(ctx context.Context, chatID int64, offset int) {
	if offset < 0 || offset > 10000 {
		return
	}
	items, err := b.store.TelegramMusicTracks(ctx, 9, offset)
	if err != nil {
		b.sendError(ctx, chatID, err)
		return
	}
	if len(items) == 0 {
		_ = b.api.sendMessage(ctx, chatID, "🎵 Музыки из Telegram пока нет. Пришлите боту аудиофайлы или документы с музыкой (до 20 МБ каждый).", b.mainKeyboard())
		return
	}
	rows := make([][]InlineKeyboardButton, 0, 11)
	for _, item := range items[:min(len(items), 8)] {
		rows = append(rows, []InlineKeyboardButton{{Text: "🎵 " + short(item.Artist+" — "+item.Title, 42), CallbackData: "track:" + strconv.FormatInt(item.ID, 10)}})
	}
	page := make([]InlineKeyboardButton, 0, 2)
	if offset > 0 {
		page = append(page, InlineKeyboardButton{Text: "← Назад", CallbackData: "music:" + strconv.Itoa(max(0, offset-8))})
	}
	if len(items) > 8 {
		page = append(page, InlineKeyboardButton{Text: "Дальше →", CallbackData: "music:" + strconv.Itoa(offset+8)})
	}
	if len(page) > 0 {
		rows = append(rows, page)
	}
	rows = append(rows, []InlineKeyboardButton{{Text: "← Меню", CallbackData: "menu:home"}})
	_ = b.api.sendMessage(ctx, chatID, "🎵 <b>Музыка</b>\nНажмите на трек для прослушивания или скачивания.", InlineKeyboardMarkup{Rows: rows})
}

func (b *Bot) showMusicTrack(ctx context.Context, chatID, id int64) {
	item, found, err := b.store.TelegramMusicByID(ctx, id)
	if err != nil {
		b.sendError(ctx, chatID, err)
		return
	}
	if !found {
		_ = b.api.sendMessage(ctx, chatID, "Трек не найден.", musicKeyboard())
		return
	}
	text := "🎵 <b>" + html.EscapeString(item.Title) + "</b>\n" + html.EscapeString(item.Artist) + " · " + html.EscapeString(item.Album) + "\n" + humanBytes(item.Size)
	_ = b.api.sendMessage(ctx, chatID, text, InlineKeyboardMarkup{Rows: [][]InlineKeyboardButton{
		{{Text: "▶️ Слушать", CallbackData: "listen:" + strconv.FormatInt(id, 10)}, {Text: "⬇️ Скачать", CallbackData: "get:" + strconv.FormatInt(id, 10)}},
		{{Text: "← Музыка", CallbackData: "music:0"}},
	}})
}

func (b *Bot) sendMusicTrack(ctx context.Context, chatID, id int64, download bool) {
	item, found, err := b.store.TelegramMusicByID(ctx, id)
	if err != nil {
		b.sendError(ctx, chatID, err)
		return
	}
	if !found {
		_ = b.api.sendMessage(ctx, chatID, "Трек не найден.", musicKeyboard())
		return
	}
	path, err := b.musicPath(item.Path)
	if err != nil {
		b.sendError(ctx, chatID, err)
		return
	}
	if download {
		err = b.api.sendStoredFile(ctx, chatID, "sendDocument", "document", path, item.Artist+" — "+item.Title)
	} else if item.FileKind == "audio" {
		err = b.api.call(ctx, "sendAudio", map[string]any{"chat_id": chatID, "audio": item.FileID, "performer": item.Artist, "title": item.Title}, nil)
	} else if ext := strings.ToLower(filepath.Ext(path)); ext == ".mp3" || ext == ".m4a" {
		err = b.api.sendStoredFile(ctx, chatID, "sendAudio", "audio", path, item.Artist+" — "+item.Title)
	} else {
		err = b.api.sendStoredFile(ctx, chatID, "sendDocument", "document", path, item.Artist+" — "+item.Title)
	}
	if err != nil {
		b.sendError(ctx, chatID, err)
	}
}

func musicKeyboard() InlineKeyboardMarkup {
	return InlineKeyboardMarkup{Rows: [][]InlineKeyboardButton{{{Text: "🎵 Музыка", CallbackData: "music:0"}}, {{Text: "← Меню", CallbackData: "menu:home"}}}}
}
