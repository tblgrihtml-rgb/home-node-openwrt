package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type apiClient struct {
	baseURL           string
	token             string
	http              *http.Client
	macFilesPrefix    string
	routerFilesPrefix string
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
}

type Update struct {
	ID       int64          `json:"update_id"`
	Message  *Message       `json:"message"`
	Callback *CallbackQuery `json:"callback_query"`
}

type Message struct {
	ID       int64         `json:"message_id"`
	From     *User         `json:"from"`
	Chat     Chat          `json:"chat"`
	Text     string        `json:"text"`
	Audio    *AudioFile    `json:"audio"`
	Document *DocumentFile `json:"document"`
}

type AudioFile struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name"`
	FileSize     int64  `json:"file_size"`
	Performer    string `json:"performer"`
	Title        string `json:"title"`
}

type DocumentFile struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name"`
	FileSize     int64  `json:"file_size"`
	MimeType     string `json:"mime_type"`
}

type TelegramFile struct {
	FilePath string `json:"file_path"`
	FileSize int64  `json:"file_size"`
}

type User struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type InlineKeyboardMarkup struct {
	Rows [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

func newAPIClient(baseURL, token, bindAddress, macFilesPrefix, routerFilesPrefix string) *apiClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	parsed, _ := url.Parse(baseURL)
	host := parsed.Hostname()
	loopback := host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
	if ip := net.ParseIP(bindAddress); ip != nil && !loopback {
		transport.DialContext = (&net.Dialer{LocalAddr: &net.TCPAddr{IP: ip}}).DialContext
	}
	return &apiClient{
		baseURL:           baseURL,
		token:             token,
		http:              &http.Client{Timeout: 40 * time.Second, Transport: transport},
		macFilesPrefix:    macFilesPrefix,
		routerFilesPrefix: routerFilesPrefix,
	}
}

func (c *apiClient) call(ctx context.Context, method string, payload any, target any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	// The token is part of the Bot API path. Network errors are deliberately
	// returned without wrapping net/http errors so the secret cannot reach logs.
	endpoint := fmt.Sprintf("%s/bot%s/%s", c.baseURL, c.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("не удалось подготовить запрос к Telegram")
	}
	req.Header.Set("Content-Type", "application/json")
	client := c.http
	if method == "getFile" && c.macFilesPrefix != "" {
		client = &http.Client{Timeout: 30 * time.Minute, Transport: c.http.Transport}
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("Telegram API недоступен")
	}
	defer resp.Body.Close()
	var result apiResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&result); err != nil {
		return errors.New("Telegram вернул некорректный ответ")
	}
	if resp.StatusCode != http.StatusOK || !result.OK {
		if result.Description == "" {
			result.Description = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return fmt.Errorf("Telegram API: %s", result.Description)
	}
	if target != nil && len(result.Result) > 0 {
		if err := json.Unmarshal(result.Result, target); err != nil {
			return errors.New("не удалось прочитать ответ Telegram")
		}
	}
	return nil
}

func (c *apiClient) sendMessage(ctx context.Context, chatID int64, text string, keyboard InlineKeyboardMarkup) error {
	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if len(keyboard.Rows) > 0 {
		payload["reply_markup"] = keyboard
	}
	return c.call(ctx, "sendMessage", payload, nil)
}

func (c *apiClient) answerCallback(ctx context.Context, id, text string) {
	payload := map[string]any{"callback_query_id": id}
	if text != "" {
		payload["text"] = text
	}
	_ = c.call(ctx, "answerCallbackQuery", payload, nil)
}

const maxMusicDownload = 20 << 20
const maxLocalMusicDownload = 4 << 30

func (c *apiClient) downloadFile(ctx context.Context, fileID, targetPath string) (int64, error) {
	var file TelegramFile
	if err := c.call(ctx, "getFile", map[string]any{"file_id": fileID}, &file); err != nil {
		return 0, err
	}
	if filepath.IsAbs(file.FilePath) {
		return c.linkLocalFile(file, targetPath)
	}
	if file.FileSize > maxMusicDownload {
		return 0, errors.New("Telegram Bot API не позволяет скачать файл больше 20 МБ")
	}
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 0, err
	}
	defer target.Close()
	parts := strings.Split(file.FilePath, "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." || strings.Contains(part, "\\") {
			return 0, errors.New("Telegram вернул некорректный путь к файлу")
		}
		parts[i] = url.PathEscape(part)
	}
	requestURL := fmt.Sprintf("%s/file/bot%s/%s", c.baseURL, c.token, strings.Join(parts, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return 0, errors.New("не удалось подготовить загрузку файла")
	}
	client := &http.Client{Timeout: 5 * time.Minute, Transport: c.http.Transport}
	resp, err := client.Do(req)
	if err != nil {
		return 0, errors.New("не удалось скачать файл из Telegram")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("Telegram не отдал файл: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxMusicDownload {
		return 0, errors.New("файл превышает 20 МБ")
	}
	n, err := io.Copy(target, io.LimitReader(resp.Body, maxMusicDownload+1))
	if err != nil {
		return 0, errors.New("загрузка файла прервалась")
	}
	if n > maxMusicDownload || (file.FileSize > 0 && n != file.FileSize) || (resp.ContentLength >= 0 && n != resp.ContentLength) {
		return 0, errors.New("файл слишком большой или получен не полностью")
	}
	return n, nil
}

func (c *apiClient) linkLocalFile(file TelegramFile, targetPath string) (int64, error) {
	if c.macFilesPrefix == "" || c.routerFilesPrefix == "" {
		return 0, errors.New("локальный Telegram API требует настроенного пути к файлам на роутере")
	}
	relative, err := filepath.Rel(c.macFilesPrefix, file.FilePath)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return 0, errors.New("файл Telegram находится вне разрешённого каталога")
	}
	root, err := filepath.EvalSymlinks(c.routerFilesPrefix)
	if err != nil {
		return 0, errors.New("каталог файлов Telegram на роутере недоступен")
	}
	source, err := filepath.EvalSymlinks(filepath.Join(root, relative))
	if err != nil {
		return 0, errors.New("загруженный файл Telegram не виден на роутере")
	}
	verified, err := filepath.Rel(root, source)
	if err != nil || verified == ".." || strings.HasPrefix(verified, ".."+string(os.PathSeparator)) {
		return 0, errors.New("файл Telegram выходит за пределы разрешённого каталога")
	}
	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() {
		return 0, errors.New("файл Telegram не найден на SSD")
	}
	if info.Size() > maxLocalMusicDownload || (file.FileSize > 0 && info.Size() != file.FileSize) {
		return 0, errors.New("файл Telegram слишком большой или ещё не полностью загружен")
	}
	if err := os.Link(source, targetPath); err == nil {
		return info.Size(), nil
	}
	input, err := os.Open(source)
	if err != nil {
		return 0, err
	}
	defer input.Close()
	output, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 0, err
	}
	defer output.Close()
	n, err := io.Copy(output, io.LimitReader(input, maxLocalMusicDownload+1))
	if err != nil || n != info.Size() {
		return 0, errors.New("не удалось скопировать файл Telegram на SSD")
	}
	return n, nil
}

func (c *apiClient) sendStoredFile(ctx context.Context, chatID int64, method, field, path, caption string) error {
	file, err := os.Open(path)
	if err != nil {
		return errors.New("музыкальный файл больше не найден")
	}
	defer file.Close()
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	go func() {
		defer writer.Close()
		if err := multipartWriter.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		if err := multipartWriter.WriteField("caption", caption); err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		part, err := multipartWriter.CreateFormFile(field, filepath.Base(path))
		if err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		if _, err = io.Copy(part, file); err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		if err = multipartWriter.Close(); err != nil {
			_ = writer.CloseWithError(err)
		}
	}()
	endpoint := fmt.Sprintf("%s/bot%s/%s", c.baseURL, c.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, reader)
	if err != nil {
		_ = reader.Close()
		return errors.New("не удалось подготовить отправку файла")
	}
	req.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	timeout := 5 * time.Minute
	if c.macFilesPrefix != "" {
		timeout = 30 * time.Minute
	}
	client := &http.Client{Timeout: timeout, Transport: c.http.Transport}
	resp, err := client.Do(req)
	if err != nil {
		_ = reader.Close()
		return errors.New("не удалось отправить файл в Telegram")
	}
	defer resp.Body.Close()
	var result apiResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return errors.New("Telegram вернул некорректный ответ")
	}
	if resp.StatusCode != http.StatusOK || !result.OK {
		return fmt.Errorf("Telegram отклонил файл: %s", result.Description)
	}
	return nil
}
