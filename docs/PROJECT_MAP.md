# Карта проекта

- `services/home-api/cmd/homenode-api` — запуск сервиса.
- `services/home-api/internal/telegram` — Bot API, приём и выдача музыки, команды бота.
- `services/home-api/internal/store` — SQLite, включая учёт импортированных треков.
- `services/home-api/internal/server` — HTTP API, поток и скачивание медиа; `manga.go` индексирует EPUB и отдаёт страницы, `devices.go` объединяет DHCP и ARP для панели устройств.
- `services/home-api/internal/store` — SQLite, включая историю видео и страниц манги.
- `services/home-api/internal/webui/static` — интерфейс и плееры: `index.html` содержит вкладки «Видео», «Манга» и «Музыка», `app.js` управляет библиотеками и читалкой, включая браузерный OCR одной страницы, `styles.css` задаёт адаптивную раскладку и экран чтения.
- `services/home-api/deploy` — пример переменных и OpenWrt-службы; реальные файлы `/etc/homenode.env` не публикуются.
- `scripts` — необязательные сетевые скрипты; запускать только после проверки своей конфигурации.
- `scripts/homenode-vpn-policy` — выборочная и полная VPN-маршрутизация LAN-клиентов по MAC-адресу с kill-switch.
- `scripts/import-windows-music.py` — разовый перенос аудио с Windows по SFTP прямо на смонтированный SSD, с SHA-256-проверкой, пропуском точных дублей и раскладкой по тегам.
- `docs` — состояние, настройка, безопасность и журнал изменений.
