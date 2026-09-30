package telegram

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"homenode/services/home-api/internal/store"
	"homenode/services/home-api/internal/transmission"
)

type Config struct {
	Token       string
	APIBaseURL  string
	AppURL      string
	InternalURL string
	APIUser     string
	APIPassword string
	BindAddress string
}

type Status struct {
	Configured    bool   `json:"configured"`
	Connected     bool   `json:"connected"`
	Paired        bool   `json:"paired"`
	BotUsername   string `json:"bot_username"`
	OwnerUsername string `json:"owner_username"`
}

type Pairing struct {
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"`
}

type Bot struct {
	cfg    Config
	api    *apiClient
	local  *localClient
	store  *store.Store
	logger *slog.Logger

	mu              sync.RWMutex
	connected       bool
	botUsername     string
	pairingCode     string
	pairingExpires  time.Time
	pairingAttempts int
	awaitingSearch  bool
	titleCache      map[string]string
}

func New(cfg Config, database *store.Store, logger *slog.Logger) *Bot {
	return &Bot{
		cfg: cfg, api: newAPIClient(cfg.APIBaseURL, cfg.Token, cfg.BindAddress),
		local: newLocalClient(cfg.InternalURL, cfg.APIUser, cfg.APIPassword),
		store: database, logger: logger, titleCache: make(map[string]string),
	}
}

func (b *Bot) Status(ctx context.Context) Status {
	owner, paired, _ := b.store.TelegramOwner(ctx)
	b.mu.RLock()
	defer b.mu.RUnlock()
	return Status{
		Configured: b.cfg.Token != "", Connected: b.connected, Paired: paired,
		BotUsername: b.botUsername, OwnerUsername: owner.Username,
	}
}

func (b *Bot) CreatePairingCode(ctx context.Context) (Pairing, error) {
	if b.cfg.Token == "" {
		return Pairing{}, errors.New("Telegram-бот не настроен")
	}
	if _, paired, err := b.store.TelegramOwner(ctx); err != nil {
		return Pairing{}, err
	} else if paired {
		return Pairing{}, errors.New("Telegram уже привязан; сначала отключите текущего владельца")
	}
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Pairing{}, err
	}
	code := fmt.Sprintf("%06d", binary.BigEndian.Uint32(raw[:])%1000000)
	expires := time.Now().Add(10 * time.Minute)
	b.mu.Lock()
	b.pairingCode, b.pairingExpires, b.pairingAttempts = code, expires, 0
	b.mu.Unlock()
	return Pairing{Code: code, ExpiresAt: expires.UTC().Format(time.RFC3339)}, nil
}

func (b *Bot) Unpair(ctx context.Context) error {
	if err := b.store.ClearTelegramOwner(ctx); err != nil {
		return err
	}
	b.mu.Lock()
	b.awaitingSearch, b.pairingCode = false, ""
	b.mu.Unlock()
	return nil
}

func (b *Bot) SendTestMessage(ctx context.Context) error {
	owner, paired, err := b.store.TelegramOwner(ctx)
	if err != nil {
		return err
	}
	if !paired {
		return errors.New("Telegram-владелец ещё не привязан")
	}
	return b.api.sendMessage(ctx, owner.ChatID,
		"✅ <b>Home Node на связи</b>\nСообщения бота проходят через VPN.", InlineKeyboardMarkup{})
}

func (b *Bot) Run(ctx context.Context) {
	if b.cfg.Token == "" {
		b.logger.Info("Telegram bot disabled: token is not configured")
		return
	}
	var me User
	startupBackoff := time.Second
	for ctx.Err() == nil {
		if err := b.api.call(ctx, "getMe", map[string]any{}, &me); err != nil {
			b.mu.Lock()
			b.connected = false
			b.mu.Unlock()
			b.logger.Warn("Telegram bot authentication failed", "error", err)
			if !wait(ctx, startupBackoff) {
				return
			}
			if startupBackoff < 30*time.Second {
				startupBackoff *= 2
			}
			continue
		}
		break
	}
	if ctx.Err() != nil {
		return
	}
	b.mu.Lock()
	b.connected, b.botUsername = true, me.Username
	b.mu.Unlock()
	_ = b.api.call(ctx, "deleteWebhook", map[string]any{"drop_pending_updates": false}, nil)
	_ = b.api.call(ctx, "setMyCommands", map[string]any{"commands": []map[string]string{
		{"command": "start", "description": "Главное меню"},
		{"command": "search", "description": "Найти аниме или фильм"},
		{"command": "downloads", "description": "Управление загрузками"},
		{"command": "library", "description": "Медиатека"},
		{"command": "app", "description": "Открыть Home Node"},
		{"command": "help", "description": "Справка"},
	}}, nil)
	b.logger.Info("Telegram bot started", "username", me.Username)
	go b.watchDownloads(ctx)

	offset, _ := b.store.TelegramOffset(ctx)
	backoff := time.Second
	for ctx.Err() == nil {
		var updates []Update
		err := b.api.call(ctx, "getUpdates", map[string]any{
			"offset": offset, "timeout": 25,
			"allowed_updates": []string{"message", "callback_query"},
		}, &updates)
		if err != nil {
			b.mu.Lock()
			b.connected = false
			b.mu.Unlock()
			b.logger.Warn("Telegram polling failed", "error", err)
			if !wait(ctx, backoff) {
				return
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		b.mu.Lock()
		b.connected = true
		b.mu.Unlock()
		backoff = time.Second
		for _, update := range updates {
			b.handleUpdate(ctx, update)
			if update.ID >= offset {
				offset = update.ID + 1
			}
		}
		if len(updates) > 0 {
			_ = b.store.SetTelegramOffset(ctx, offset)
		}
	}
}

func (b *Bot) watchDownloads(ctx context.Context) {
	// The first snapshot becomes the baseline, so already completed torrents do
	// not produce a burst of old notifications after an update or reboot.
	states, err := b.store.TelegramDownloadStates(ctx)
	if err != nil {
		b.logger.Warn("load Telegram download states", "error", err)
		states = make(map[int64]store.TelegramDownloadState)
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		items, err := b.local.downloads(ctx)
		if err != nil {
			continue
		}
		owner, paired, ownerErr := b.store.TelegramOwner(ctx)
		for _, item := range items {
			previous, known := states[item.ID]
			current := store.TelegramDownloadState{PercentDone: item.PercentDone, ErrorString: item.ErrorString}
			completed, failed, shouldStop := classifyDownload(previous, known, item)
			stopped := false
			if shouldStop {
				if err := b.local.control(ctx, item.ID, "stop"); err != nil {
					b.logger.Warn("stop completed torrent", "id", item.ID, "error", err)
				} else {
					stopped = true
				}
			}
			if ownerErr == nil && paired && completed {
				status := "Торрент остановлен и больше не раздаётся."
				if !stopped && item.Status != 0 {
					status = "Файлы готовы, но автоматически остановить торрент не удалось."
				}
				_ = b.api.sendMessage(ctx, owner.ChatID,
					"✅ <b>Загрузка завершена</b>\n"+html.EscapeString(item.Name)+"\n\n"+status,
					InlineKeyboardMarkup{Rows: [][]InlineKeyboardButton{{{Text: "🎞 Медиатека", CallbackData: "menu:library"}}}})
			}
			if ownerErr == nil && paired && failed {
				_ = b.api.sendMessage(ctx, owner.ChatID,
					"⚠️ <b>Ошибка загрузки</b>\n"+html.EscapeString(item.Name)+"\n"+html.EscapeString(item.ErrorString),
					downloadsKeyboard())
			}
			if !known || previous != current {
				states[item.ID] = current
				_ = b.store.SetTelegramDownloadState(ctx, item.ID, current)
			}
		}
	}
}

func classifyDownload(previous store.TelegramDownloadState, known bool, item transmission.Torrent) (completed, failed, shouldStop bool) {
	completed = known && previous.PercentDone < 1 && item.PercentDone >= 1
	// Tracker errors after 100% do not affect downloaded files and should not
	// produce alarming notifications. Completed active torrents are stopped so
	// they cannot keep uploading or contacting an obsolete tracker.
	failed = known && item.PercentDone < 1 && item.ErrorString != "" && item.ErrorString != previous.ErrorString
	shouldStop = item.PercentDone >= 1 && item.Status != 0
	return completed, failed, shouldStop
}

func (b *Bot) handleUpdate(ctx context.Context, update Update) {
	if update.Message != nil && update.Message.From != nil {
		b.handleMessage(ctx, update.Message)
		return
	}
	if update.Callback != nil && update.Callback.Message != nil {
		b.handleCallback(ctx, update.Callback)
	}
}

func (b *Bot) handleMessage(ctx context.Context, message *Message) {
	if message.Chat.Type != "private" {
		return
	}
	text := strings.TrimSpace(message.Text)
	if strings.HasPrefix(text, "/pair ") {
		b.pair(ctx, message, strings.TrimSpace(strings.TrimPrefix(text, "/pair ")))
		return
	}
	if !b.authorized(ctx, message.From.ID, message.Chat.ID) {
		_ = b.api.sendMessage(ctx, message.Chat.ID,
			"🔒 <b>Home Node не привязан</b>\n\nОткройте локальный интерфейс Home Node → Telegram и получите одноразовый код.", InlineKeyboardMarkup{})
		return
	}
	command, argument := splitCommand(text)
	switch command {
	case "/start", "/help", "":
		b.mu.RLock()
		awaiting := b.awaitingSearch
		b.mu.RUnlock()
		if command == "" && awaiting {
			b.mu.Lock()
			b.awaitingSearch = false
			b.mu.Unlock()
			b.search(ctx, message.Chat.ID, text)
			return
		}
		b.mainMenu(ctx, message.Chat.ID)
	case "/search":
		if argument == "" {
			b.mu.Lock()
			b.awaitingSearch = true
			b.mu.Unlock()
			_ = b.api.sendMessage(ctx, message.Chat.ID, "🔍 Напишите название аниме, фильма или сериала.", cancelKeyboard())
			return
		}
		b.search(ctx, message.Chat.ID, argument)
	case "/downloads":
		b.showDownloads(ctx, message.Chat.ID)
	case "/library":
		b.showLibrary(ctx, message.Chat.ID)
	case "/app":
		b.openApp(ctx, message.Chat.ID)
	default:
		_ = b.api.sendMessage(ctx, message.Chat.ID, "Не понял команду. Используйте меню ниже.", b.mainKeyboard())
	}
}

func (b *Bot) pair(ctx context.Context, message *Message, code string) {
	if _, paired, _ := b.store.TelegramOwner(ctx); paired {
		_ = b.api.sendMessage(ctx, message.Chat.ID, "Бот уже привязан к владельцу.", InlineKeyboardMarkup{})
		return
	}
	b.mu.Lock()
	valid := time.Now().Before(b.pairingExpires) && b.pairingAttempts < 5 &&
		len(code) == len(b.pairingCode) && subtle.ConstantTimeCompare([]byte(code), []byte(b.pairingCode)) == 1
	b.pairingAttempts++
	if valid {
		b.pairingCode = ""
	}
	b.mu.Unlock()
	if !valid {
		_ = b.api.sendMessage(ctx, message.Chat.ID, "Код неверный или устарел. Создайте новый код в Home Node.", InlineKeyboardMarkup{})
		return
	}
	username := message.From.Username
	if username == "" {
		username = message.From.FirstName
	}
	err := b.store.SetTelegramOwner(ctx, store.TelegramOwner{
		UserID: message.From.ID, ChatID: message.Chat.ID, Username: username,
		PairedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		b.logger.Error("save Telegram owner", "error", err)
		_ = b.api.sendMessage(ctx, message.Chat.ID, "Не удалось сохранить привязку.", InlineKeyboardMarkup{})
		return
	}
	_ = b.api.sendMessage(ctx, message.Chat.ID, "✅ <b>Home Node подключён</b>\n\nТеперь можно искать медиа и управлять загрузками.", b.mainKeyboard())
}

func (b *Bot) handleCallback(ctx context.Context, callback *CallbackQuery) {
	chatID := callback.Message.Chat.ID
	if !b.authorized(ctx, callback.From.ID, chatID) {
		b.api.answerCallback(ctx, callback.ID, "Нет доступа")
		return
	}
	b.api.answerCallback(ctx, callback.ID, "")
	parts := strings.Split(callback.Data, ":")
	switch parts[0] {
	case "menu":
		if len(parts) != 2 {
			return
		}
		switch parts[1] {
		case "search":
			b.mu.Lock()
			b.awaitingSearch = true
			b.mu.Unlock()
			_ = b.api.sendMessage(ctx, chatID, "🔍 Напишите название аниме, фильма или сериала.", cancelKeyboard())
		case "downloads":
			b.showDownloads(ctx, chatID)
		case "library":
			b.showLibrary(ctx, chatID)
		case "app":
			b.openApp(ctx, chatID)
		case "home":
			b.mainMenu(ctx, chatID)
		}
	case "a":
		if id, ok := callbackInt(parts, 1); ok {
			b.showAnimeTorrents(ctx, chatID, id)
		}
	case "t":
		if releaseID, ok := callbackInt(parts, 1); ok {
			if torrentID, ok := callbackInt(parts, 2); ok {
				b.downloadAnime(ctx, chatID, releaseID, torrentID)
			}
		}
	case "r":
		if id, ok := callbackInt(parts, 1); ok {
			b.downloadTracker(ctx, chatID, id)
		}
	case "d":
		if id, ok := callbackInt(parts, 1); ok {
			b.showDownload(ctx, chatID, id)
		}
	case "c":
		if len(parts) == 3 {
			if id, err := strconv.ParseInt(parts[2], 10, 64); err == nil {
				b.controlDownload(ctx, chatID, parts[1], id)
			}
		}
	}
}

func (b *Bot) mainMenu(ctx context.Context, chatID int64) {
	b.mu.Lock()
	b.awaitingSearch = false
	b.mu.Unlock()
	_ = b.api.sendMessage(ctx, chatID,
		"🏠 <b>Home Node</b>\n\nПоиск по AniLiberty и RuTracker, медиатека и управление Transmission.", b.mainKeyboard())
}

func (b *Bot) mainKeyboard() InlineKeyboardMarkup {
	return InlineKeyboardMarkup{Rows: [][]InlineKeyboardButton{
		{{Text: "🔍 Поиск", CallbackData: "menu:search"}},
		{{Text: "⬇️ Загрузки", CallbackData: "menu:downloads"}, {Text: "🎞 Медиатека", CallbackData: "menu:library"}},
		{{Text: "📱 Открыть Home Node", CallbackData: "menu:app"}},
	}}
}

func (b *Bot) openApp(ctx context.Context, chatID int64) {
	_ = b.api.sendMessage(ctx, chatID,
		"📱 <b>Home Node</b>\n\nОткройте адрес внутри домашней сети:\n"+html.EscapeString(b.cfg.AppURL),
		InlineKeyboardMarkup{Rows: [][]InlineKeyboardButton{{{Text: "← Меню", CallbackData: "menu:home"}}}})
}

func (b *Bot) search(ctx context.Context, chatID int64, query string) {
	query = strings.TrimSpace(query)
	if utf8.RuneCountInString(query) < 2 || utf8.RuneCountInString(query) > 100 {
		_ = b.api.sendMessage(ctx, chatID, "Запрос должен содержать от 2 до 100 символов.", b.mainKeyboard())
		return
	}
	_ = b.api.sendMessage(ctx, chatID, "Ищу «"+html.EscapeString(query)+"»…", InlineKeyboardMarkup{})
	type animeResult struct {
		items []struct {
			id    int64
			title string
		}
		err error
	}
	animeCh := make(chan animeResult, 1)
	type trackerResult struct {
		items []struct {
			id, seeds int64
			title     string
		}
		err error
	}
	trackerCh := make(chan trackerResult, 1)
	go func() {
		items, err := b.local.searchAnime(ctx, query)
		result := animeResult{err: err}
		for _, item := range items {
			result.items = append(result.items, struct {
				id    int64
				title string
			}{item.ID, item.Name.Main})
		}
		animeCh <- result
	}()
	go func() {
		items, err := b.local.searchTracker(ctx, query)
		result := trackerResult{err: err}
		for _, item := range items {
			result.items = append(result.items, struct {
				id, seeds int64
				title     string
			}{item.TopicID, int64(item.Seeders), item.Title})
		}
		trackerCh <- result
	}()
	anime, tracker := <-animeCh, <-trackerCh
	rows := make([][]InlineKeyboardButton, 0, 11)
	lines := []string{"🔍 <b>Результаты поиска</b>"}
	b.mu.Lock()
	for i, item := range anime.items {
		if i >= 5 {
			break
		}
		key := "a:" + strconv.FormatInt(item.id, 10)
		b.titleCache[key] = item.title
		rows = append(rows, []InlineKeyboardButton{{Text: "AniLiberty · " + short(item.title, 40), CallbackData: key}})
	}
	for i, item := range tracker.items {
		if i >= 5 {
			break
		}
		key := "r:" + strconv.FormatInt(item.id, 10)
		b.titleCache[key] = item.title
		rows = append(rows, []InlineKeyboardButton{{Text: fmt.Sprintf("RuTracker · %s · %d↑", short(item.title, 31), item.seeds), CallbackData: key}})
	}
	b.mu.Unlock()
	if anime.err != nil {
		lines = append(lines, "\nAniLiberty: "+html.EscapeString(anime.err.Error()))
	}
	if tracker.err != nil {
		lines = append(lines, "\nRuTracker: "+html.EscapeString(tracker.err.Error()))
	}
	if len(rows) == 0 {
		lines = append(lines, "\nНичего не найдено.")
	}
	rows = append(rows, []InlineKeyboardButton{{Text: "← Меню", CallbackData: "menu:home"}})
	_ = b.api.sendMessage(ctx, chatID, strings.Join(lines, ""), InlineKeyboardMarkup{Rows: rows})
}

func (b *Bot) showAnimeTorrents(ctx context.Context, chatID, releaseID int64) {
	items, err := b.local.animeTorrents(ctx, releaseID)
	if err != nil {
		b.sendError(ctx, chatID, err)
		return
	}
	rows := make([][]InlineKeyboardButton, 0, 9)
	for i, item := range items {
		if i >= 8 {
			break
		}
		label := item.Quality.Description
		if label == "" {
			label = item.Quality.Value
		}
		rows = append(rows, []InlineKeyboardButton{{
			Text:         fmt.Sprintf("%s · %s · %d↑ · %s", short(label, 14), short(item.Codec.Label, 10), item.Seeders, humanBytes(item.Size)),
			CallbackData: fmt.Sprintf("t:%d:%d", releaseID, item.ID),
		}})
	}
	rows = append(rows, []InlineKeyboardButton{{Text: "← Меню", CallbackData: "menu:home"}})
	_ = b.api.sendMessage(ctx, chatID, "Выберите качество и раздачу:", InlineKeyboardMarkup{Rows: rows})
}

func (b *Bot) downloadAnime(ctx context.Context, chatID, releaseID, torrentID int64) {
	title := b.cachedTitle("a:" + strconv.FormatInt(releaseID, 10))
	if title == "" {
		title = "Аниме " + strconv.FormatInt(releaseID, 10)
	}
	if err := b.local.downloadAnime(ctx, releaseID, torrentID, title); err != nil {
		b.sendError(ctx, chatID, err)
		return
	}
	_ = b.api.sendMessage(ctx, chatID, "✅ «"+html.EscapeString(title)+"» добавлено в загрузки.", downloadsKeyboard())
}

func (b *Bot) downloadTracker(ctx context.Context, chatID, topicID int64) {
	title := b.cachedTitle("r:" + strconv.FormatInt(topicID, 10))
	if title == "" {
		_ = b.api.sendMessage(ctx, chatID, "Результат поиска устарел. Повторите поиск.", b.mainKeyboard())
		return
	}
	if err := b.local.downloadTracker(ctx, topicID, title); err != nil {
		b.sendError(ctx, chatID, err)
		return
	}
	_ = b.api.sendMessage(ctx, chatID, "✅ «"+html.EscapeString(title)+"» добавлено в загрузки.", downloadsKeyboard())
}

func (b *Bot) showDownloads(ctx context.Context, chatID int64) {
	items, err := b.local.downloads(ctx)
	if err != nil {
		b.sendError(ctx, chatID, err)
		return
	}
	rows := make([][]InlineKeyboardButton, 0, 9)
	for i, item := range items {
		if i >= 8 {
			break
		}
		rows = append(rows, []InlineKeyboardButton{{
			Text:         fmt.Sprintf("%d%% · %s", int(item.PercentDone*100), short(item.Name, 38)),
			CallbackData: "d:" + strconv.FormatInt(item.ID, 10),
		}})
	}
	if len(items) == 0 {
		_ = b.api.sendMessage(ctx, chatID, "⬇️ Загрузок пока нет.", b.mainKeyboard())
		return
	}
	rows = append(rows, []InlineKeyboardButton{{Text: "↻ Обновить", CallbackData: "menu:downloads"}, {Text: "← Меню", CallbackData: "menu:home"}})
	_ = b.api.sendMessage(ctx, chatID, "⬇️ <b>Загрузки</b>\nВыберите задачу для управления:", InlineKeyboardMarkup{Rows: rows})
}

func (b *Bot) showDownload(ctx context.Context, chatID, id int64) {
	items, err := b.local.downloads(ctx)
	if err != nil {
		b.sendError(ctx, chatID, err)
		return
	}
	var item *transmission.Torrent
	for i := range items {
		if items[i].ID == id {
			item = &items[i]
			break
		}
	}
	if item == nil {
		_ = b.api.sendMessage(ctx, chatID, "Загрузка уже удалена.", downloadsKeyboard())
		return
	}
	action, label := "p", "⏸ Пауза"
	if item.Status == 0 {
		action, label = "s", "▶️ Продолжить"
	}
	text := fmt.Sprintf("⬇️ <b>%s</b>\n\nГотово: %d%%\nСкорость: %s/с\nПиры: %d\nРазмер: %s",
		html.EscapeString(item.Name), int(item.PercentDone*100), humanBytes(item.RateDownload), item.PeersConnected, humanBytes(item.TotalSize))
	if item.ErrorString != "" {
		text += "\nОшибка: " + html.EscapeString(item.ErrorString)
	}
	keyboard := InlineKeyboardMarkup{Rows: [][]InlineKeyboardButton{
		{{Text: label, CallbackData: fmt.Sprintf("c:%s:%d", action, id)}},
		{{Text: "🗑 Убрать задачу", CallbackData: fmt.Sprintf("c:r:%d", id)}},
		{{Text: "❌ Удалить вместе с файлами", CallbackData: fmt.Sprintf("c:x:%d", id)}},
		{{Text: "← Загрузки", CallbackData: "menu:downloads"}},
	}}
	_ = b.api.sendMessage(ctx, chatID, text, keyboard)
}

func (b *Bot) controlDownload(ctx context.Context, chatID int64, action string, id int64) {
	if action == "x" {
		_ = b.api.sendMessage(ctx, chatID,
			"⚠️ <b>Удалить задачу и все скачанные файлы?</b>\nЭто действие нельзя отменить.",
			InlineKeyboardMarkup{Rows: [][]InlineKeyboardButton{
				{{Text: "Да, удалить файлы", CallbackData: fmt.Sprintf("c:y:%d", id)}},
				{{Text: "Отмена", CallbackData: fmt.Sprintf("d:%d", id)}},
			}})
		return
	}
	apiAction, message := map[string]string{
		"s": "start", "p": "stop", "r": "remove", "y": "remove-data",
	}[action], map[string]string{
		"s": "Загрузка продолжена.", "p": "Загрузка приостановлена.",
		"r": "Задача удалена, файлы сохранены.", "y": "Задача и файлы удалены.",
	}[action]
	if apiAction == "" {
		return
	}
	if err := b.local.control(ctx, id, apiAction); err != nil {
		b.sendError(ctx, chatID, err)
		return
	}
	_ = b.api.sendMessage(ctx, chatID, "✅ "+message, downloadsKeyboard())
}

func (b *Bot) showLibrary(ctx context.Context, chatID int64) {
	items, err := b.local.library(ctx)
	if err != nil {
		b.sendError(ctx, chatID, err)
		return
	}
	if len(items) == 0 {
		_ = b.api.sendMessage(ctx, chatID, "🎞 Медиатека пока пуста.", b.mainKeyboard())
		return
	}
	lines := []string{"🎞 <b>Медиатека</b>"}
	for i, item := range items {
		if i >= 12 {
			break
		}
		category := "Фильм"
		if item.Category == "anime" {
			category = "Аниме"
		} else if item.Category == "music" {
			category = "Музыка"
		}
		lines = append(lines, fmt.Sprintf("\n• <b>%s</b> — %s, %d файл(ов), %s",
			html.EscapeString(item.Title), category, item.FileCount, humanBytes(item.Size)))
	}
	_ = b.api.sendMessage(ctx, chatID, strings.Join(lines, ""), InlineKeyboardMarkup{Rows: [][]InlineKeyboardButton{
		{{Text: "📱 Открыть медиатеку", CallbackData: "menu:app"}},
		{{Text: "← Меню", CallbackData: "menu:home"}},
	}})
}

func (b *Bot) authorized(ctx context.Context, userID, chatID int64) bool {
	owner, ok, err := b.store.TelegramOwner(ctx)
	return err == nil && ok && owner.UserID == userID && owner.ChatID == chatID
}

func (b *Bot) cachedTitle(key string) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.titleCache[key]
}

func (b *Bot) sendError(ctx context.Context, chatID int64, err error) {
	b.logger.Warn("Telegram command failed", "error", err)
	_ = b.api.sendMessage(ctx, chatID, "Не удалось выполнить действие: "+html.EscapeString(err.Error()), b.mainKeyboard())
}

func splitCommand(text string) (string, string) {
	if !strings.HasPrefix(text, "/") {
		return "", ""
	}
	parts := strings.SplitN(text, " ", 2)
	command := strings.SplitN(parts[0], "@", 2)[0]
	if len(parts) == 2 {
		return command, strings.TrimSpace(parts[1])
	}
	return command, ""
}

func callbackInt(parts []string, index int) (int64, bool) {
	if index >= len(parts) {
		return 0, false
	}
	value, err := strconv.ParseInt(parts[index], 10, 64)
	return value, err == nil && value > 0
}

func short(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}

func humanBytes(value int64) string {
	units := []string{"Б", "КБ", "МБ", "ГБ", "ТБ"}
	number, unit := float64(value), 0
	for number >= 1024 && unit < len(units)-1 {
		number /= 1024
		unit++
	}
	if unit >= 2 {
		return fmt.Sprintf("%.1f %s", number, units[unit])
	}
	return fmt.Sprintf("%.0f %s", number, units[unit])
}

func downloadsKeyboard() InlineKeyboardMarkup {
	return InlineKeyboardMarkup{Rows: [][]InlineKeyboardButton{
		{{Text: "⬇️ Открыть загрузки", CallbackData: "menu:downloads"}},
		{{Text: "← Меню", CallbackData: "menu:home"}},
	}}
}

func cancelKeyboard() InlineKeyboardMarkup {
	return InlineKeyboardMarkup{Rows: [][]InlineKeyboardButton{{{Text: "Отмена", CallbackData: "menu:home"}}}}
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
