package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

type apiClient struct {
	baseURL string
	token   string
	http    *http.Client
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
	ID   int64  `json:"message_id"`
	From *User  `json:"from"`
	Chat Chat   `json:"chat"`
	Text string `json:"text"`
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

func newAPIClient(baseURL, token, bindAddress string) *apiClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if ip := net.ParseIP(bindAddress); ip != nil {
		transport.DialContext = (&net.Dialer{LocalAddr: &net.TCPAddr{IP: ip}}).DialContext
	}
	return &apiClient{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{Timeout: 40 * time.Second, Transport: transport},
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
	resp, err := c.http.Do(req)
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
