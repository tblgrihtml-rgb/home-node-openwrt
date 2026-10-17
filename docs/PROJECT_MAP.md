# Карта проекта

- `services/home-api/cmd/homenode-api` — запуск сервиса.
- `services/home-api/internal/telegram` — Bot API, музыка, команды бота и периодическая сверка новых серий.
- `services/home-api/internal/store` — SQLite, включая учёт импортированных треков и последней серии по каждому AniLiberty-релизу.
- `services/home-api/internal/server` — HTTP API, поток и скачивание медиа; `manga.go` индексирует EPUB и отдаёт страницы, `devices.go` объединяет DHCP и ARP для панели устройств.
- Папки медиатеки на SSD задаются в окружении: аниме (`HOMENODE_ANIME_DIR`), фильмы (`HOMENODE_MOVIES_DIR`), манга (`HOMENODE_MANGA_DIR`) и музыка (`HOMENODE_MUSIC_DIR`).
- `services/home-api/internal/store` — SQLite, включая историю видео и страниц манги.
- `services/home-api/internal/music` — поиск отдельных треков, одноразовая проверка стороннего источника, безопасное скачивание FLAC и последовательный загрузчик очереди.
- `services/home-api/internal/store/music_jobs.go` — таблица очереди музыки и переходы состояний, которые переживают перезапуск.
- `services/home-api/internal/server/music.go` — авторизованные маршруты музыкального поиска и очереди; одноразовый callback внешней проверки.
- `services/home-api/internal/webui/static` — интерфейс и плееры: `index.html` содержит вкладки «Видео», «Манга» и «Музыка», `app.js` управляет библиотеками и читалкой, `styles.css` задаёт адаптивную раскладку и экран чтения.
- `services/home-api/deploy` — пример переменных и OpenWrt-службы; реальные файлы `/etc/homenode.env` не публикуются.
- `scripts` — необязательные сетевые скрипты; запускать только после проверки своей конфигурации.
- `scripts/homenode-vpn-policy` — выборочная и полная VPN-маршрутизация LAN-клиентов по MAC-адресу с kill-switch.
- `scripts/import-windows-music.py` — разовый перенос аудио с Windows по SFTP прямо на смонтированный SSD, с SHA-256-проверкой, пропуском точных дублей и раскладкой по тегам.
- `docs` — состояние, настройка, безопасность и журнал изменений.
- `docs/THIRD_PARTY_NOTICES.md` — обязательное лицензионное уведомление для адаптированного кода SpotiFLAC.
