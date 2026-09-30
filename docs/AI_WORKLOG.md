# Журнал работ

## 2026-09-30 — Временный импорт музыки из Telegram

Что сделано: добавлен приём аудио/музыкальных документов только от привязанного владельца, ограничение 20 МБ, сортировка по тегам, защита путей, учёт дубликатов, меню `/music`, кнопки прослушивания и скачивания, скачивание музыки через сайт.

Изменённые части: `internal/telegram`, `internal/store`, `internal/server/media_stream.go`, `internal/webui/static`, `deploy/homenode.init`, README и `docs/`.

Проверка: `go test ./services/home-api/...`, `go vet ./services/home-api/...`, `node --check services/home-api/internal/webui/static/app.js`, `sh -n services/home-api/deploy/homenode.init`.

Риски: реальный импорт зависит от доступа Telegram через VPN, прав записи на SSD и лимита Bot API. Секреты и реальные аудиофайлы в Git не добавлялись.

После уточнения владельца выяснилось, что его файлы больше 20 МБ. Поэтому эту версию не развёртывали на роутере; дальнейший способ импорта требуется выбрать отдельно.
