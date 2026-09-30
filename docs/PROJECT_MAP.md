# Карта проекта

- `services/home-api/cmd/homenode-api` — запуск сервиса.
- `services/home-api/internal/telegram` — Bot API, приём и выдача музыки, команды бота.
- `services/home-api/internal/store` — SQLite, включая учёт импортированных треков.
- `services/home-api/internal/server` — HTTP API, поток и скачивание медиа.
- `services/home-api/internal/webui/static` — интерфейс и плеер.
- `services/home-api/deploy` — пример переменных и OpenWrt-службы; реальные файлы `/etc/homenode.env` не публикуются.
- `scripts` — необязательные сетевые скрипты; запускать только после проверки своей конфигурации.
- `scripts/import-windows-music.py` — разовый перенос аудио с Windows по SFTP прямо на смонтированный SSD, с SHA-256-проверкой, пропуском точных дублей и раскладкой по тегам.
- `docs` — состояние, настройка, безопасность и журнал изменений.
