const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];

const state = {
  source: 'all', library: [], libraryCategory: 'all', downloads: [], downloadFilter: 'all', selectedDownloadID: null,
  watchProgress: [], watchProgressByPath: new Map(),
  playingItem: null, playingFile: null, playingButton: null, playingPlayer: null, playingOffset: 0, lastProgressWrite: 0,
  autoplayTimer: null, autoplayRemaining: 0,
};

const isAppleWebKit = /AppleWebKit/i.test(navigator.userAgent)
  && (/iPhone|iPad|iPod/i.test(navigator.userAgent)
    || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)
    || (/Safari/i.test(navigator.userAgent) && !/Chrome|Chromium|Edg/i.test(navigator.userAgent)));
const isTailscaleHTTPS = location.protocol === 'https:' && /\.ts\.net$/i.test(location.hostname);

const api = async (path, options = {}) => {
  const response = await fetch(path, options);
  const payload = await response.json().catch(() => ({}));
  if (response.status === 401) {
    window.location.replace('/login');
    throw new Error('Сеанс входа истёк');
  }
  if (!response.ok) throw new Error(payload.error || `HTTP ${response.status}`);
  return payload;
};

const element = (tag, className, text) => {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
};

const icon = (name, alt = '') => {
  const image = element('img');
  image.src = `/icons/${name}.svg`;
  image.alt = alt;
  return image;
};

const humanBytes = (bytes = 0) => {
  const units = ['Б', 'КБ', 'МБ', 'ГБ', 'ТБ'];
  let value = Number(bytes) || 0;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit += 1; }
  return `${value.toFixed(unit > 1 ? 1 : 0)} ${units[unit]}`;
};

const humanRate = (bytes = 0) => `${humanBytes(bytes)}/с`;
const percentage = (item) => Math.round(Math.max(0, Math.min(1, item.percentDone || 0)) * 100);
const playbackTime = (seconds = 0) => {
  const total = Math.max(0, Math.floor(Number(seconds) || 0));
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const remaining = total % 60;
  return hours ? `${hours}:${String(minutes).padStart(2, '0')}:${String(remaining).padStart(2, '0')}` : `${minutes}:${String(remaining).padStart(2, '0')}`;
};

const toast = (message) => {
  const node = $('#toast');
  node.textContent = message;
  node.classList.add('show');
  clearTimeout(window.homeNodeToastTimer);
  window.homeNodeToastTimer = window.setTimeout(() => node.classList.remove('show'), 3400);
};

const emptyState = (message, detail = '', error = false) => {
  const node = element('div', `empty-state${error ? ' error-state' : ''}`);
  node.append(icon(error ? 'trash' : 'library'));
  node.append(element('strong', '', message));
  if (detail) node.append(element('span', '', detail));
  return node;
};

const poster = (url, title, className) => {
  if (url) {
    const image = element('img', className);
    image.loading = 'lazy';
    image.alt = `Постер «${title}»`;
    image.src = url.startsWith('http') ? url : `https://aniliberty.top${url}`;
    return image;
  }
  const fallback = element('div', `${className} poster-fallback`, (title || '?').slice(0, 1).toUpperCase());
  fallback.setAttribute('aria-label', `Нет постера для «${title}»`);
  return fallback;
};

const safeCopy = async (value) => {
  try {
    await navigator.clipboard.writeText(value);
  } catch (_) {
    const input = element('textarea');
    input.value = value;
    input.style.position = 'fixed';
    input.style.opacity = '0';
    document.body.append(input);
    input.select();
    document.execCommand('copy');
    input.remove();
  }
};

function setView(name) {
  $$('.view').forEach((view) => view.classList.toggle('active', view.id === `view-${name}`));
  $$('[data-view]').forEach((button) => button.classList.toggle('active', button.dataset.view === name));
  window.history.replaceState(null, '', `#${name}`);
  if (name === 'library') loadLibrary();
  if (name === 'downloads') loadDownloads();
  if (name === 'history') loadHistory();
  if (name === 'devices') loadDevices();
  if (name === 'telegram') loadTelegram();
  window.scrollTo({top: 0, behavior: 'smooth'});
}

$$('[data-view]').forEach((button) => button.addEventListener('click', () => setView(button.dataset.view)));
$$('[data-nav]').forEach((link) => link.addEventListener('click', (event) => { event.preventDefault(); setView(link.dataset.nav); }));
$$('[data-go]').forEach((button) => button.addEventListener('click', () => setView(button.dataset.go)));

$$('.source-button').forEach((button) => button.addEventListener('click', () => {
  state.source = button.dataset.source;
  $$('.source-button').forEach((item) => item.classList.toggle('active', item === button));
}));

$('#toggle-filters').addEventListener('click', () => {
  const panel = $('#filter-panel');
  panel.hidden = !panel.hidden;
  $('#toggle-filters').setAttribute('aria-expanded', String(!panel.hidden));
});

const filterInputs = ['#filter-type', '#filter-year-from', '#filter-year-to', '#filter-status', '#filter-seeds'];
function updateFilterCount() {
  const count = filterInputs.filter((selector) => {
    const value = $(selector).value;
    return value !== '' && value !== '0';
  }).length;
  const badge = $('#filter-count');
  badge.hidden = count === 0;
  badge.textContent = String(count);
}
filterInputs.forEach((selector) => $(selector).addEventListener('change', updateFilterCount));
$('#reset-filters').addEventListener('click', () => {
  $('#filter-type').value = '';
  $('#filter-year-from').value = '';
  $('#filter-year-to').value = '';
  $('#filter-status').value = '';
  $('#filter-sorting').value = 'RATING_DESC';
  $('#filter-seeds').value = '0';
  updateFilterCount();
});

function animeSearchURL(query) {
  const values = new URLSearchParams({q: query, sorting: $('#filter-sorting').value});
  const fields = [['type', '#filter-type'], ['year_from', '#filter-year-from'], ['year_to', '#filter-year-to'], ['status', '#filter-status']];
  fields.forEach(([name, selector]) => { if ($(selector).value) values.set(name, $(selector).value); });
  return `/api/anime/catalog?${values.toString()}`;
}

$('#search-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const query = $('#search-query').value.trim();
  const results = $('#search-results');
  $('#search-results-section').hidden = false;
  $('#search-message').hidden = true;
  results.replaceChildren(emptyState('Ищем по медиатекам…', 'Обычно это занимает несколько секунд.'));
  const selectedSource = state.source;
  const searches = [];
  if (selectedSource === 'all' || selectedSource === 'anime') searches.push(api(animeSearchURL(query)).then((payload) => ({source: 'anime', items: payload.data || []})));
  if (selectedSource === 'all' || selectedSource === 'tracker' || selectedSource === 'music') {
    searches.push(api(`/api/tracker/search?q=${encodeURIComponent(query)}`).then((items) => ({source: 'tracker', category: selectedSource === 'music' ? 'music' : 'movies', items})));
  }
  const settled = await Promise.allSettled(searches);
  const found = settled.filter((entry) => entry.status === 'fulfilled').map((entry) => entry.value);
  const failures = settled.filter((entry) => entry.status === 'rejected').map((entry) => entry.reason?.message || 'Источник недоступен');
  results.replaceChildren();
  let count = 0;
  found.forEach((group) => {
    let items = group.items;
    if (group.source === 'tracker') {
      const minimumSeeds = Number($('#filter-seeds').value) || 0;
      items = items.filter((item) => item.seeders >= minimumSeeds).sort((left, right) => right.seeders - left.seeders);
    }
    items.forEach((item) => {
      results.append(group.source === 'anime' ? renderAnimeResult(item) : renderTrackerResult(item, group.category));
      count += 1;
    });
  });
  if (!count) results.append(emptyState('Ничего не найдено', 'Попробуйте другое название или сбросьте фильтры.'));
  $('#search-results-title').textContent = count ? `Найдено: ${count}` : 'Результатов нет';
  $('#search-summary').textContent = selectedSource === 'all' ? 'AniLiberty + RuTracker' : (selectedSource === 'anime' ? 'AniLiberty' : (selectedSource === 'music' ? 'RuTracker · музыка' : 'RuTracker'));
  if (failures.length) {
    const message = $('#search-message');
    message.textContent = `Один источник пока недоступен: ${failures.join(' · ')}`;
    message.hidden = false;
  }
});

function renderAnimeResult(release) {
  const card = element('article', 'result-card');
  const posterPath = release.poster?.optimized?.src || release.poster?.optimized?.preview || release.poster?.src || release.poster?.preview || release.poster?.optimized?.thumbnail || release.poster?.thumbnail;
  card.append(poster(posterPath, release.name?.main || release.alias, 'result-poster'));
  const body = element('div');
  body.append(element('span', 'source-badge', 'AniLiberty'));
  body.append(element('h3', '', release.name?.main || release.alias));
  const genres = (release.genres || []).slice(0, 2).map((genre) => genre.name).join(', ');
  const details = [release.year, release.type?.description, release.age_rating?.label, release.episodes_total ? `${release.episodes_total} эп.` : '', genres].filter(Boolean);
  body.append(element('div', 'result-meta', details.join(' · ')));
  const actions = element('div', 'result-actions');
  const torrents = element('button', 'secondary-button', 'Выбрать торрент');
  torrents.addEventListener('click', () => loadAnimeTorrents(card, release, torrents));
  actions.append(torrents);
  body.append(actions);
  card.append(body);
  return card;
}

async function loadAnimeTorrents(card, release, button) {
  $('.torrent-options', card)?.remove();
  button.disabled = true;
  button.textContent = 'Загрузка…';
  const list = element('div', 'torrent-options');
  card.append(list);
  try {
    const torrents = await api(`/api/anime/${release.id}/torrents`);
    if (!torrents.length) list.append(emptyState('Торрентов пока нет'));
    torrents.forEach((torrent) => {
      const row = element('div', 'torrent-option');
      const info = element('div');
      info.append(element('strong', '', torrent.label || torrent.quality?.description || 'Раздача'));
      info.append(element('div', 'meta', `${humanBytes(torrent.size)} · ${torrent.seeders} сидов · ${torrent.codec?.label || torrent.codec?.value || ''}`));
      const download = element('button', 'primary-button', 'Скачать');
      download.addEventListener('click', async () => {
        download.disabled = true;
        try {
          await api('/api/anime/download', {
            method: 'POST', headers: {'Content-Type': 'application/json', 'X-HomeNode-Request': '1'},
            body: JSON.stringify({release_id: release.id, torrent_id: torrent.id, title: release.name?.main || torrent.label}),
          });
          toast('Добавлено в загрузки');
          await loadDownloads(true);
        } catch (error) { toast(error.message); }
        finally { download.disabled = false; }
      });
      row.append(info, download);
      list.append(row);
    });
  } catch (error) { list.replaceChildren(emptyState('Не удалось получить торренты', error.message, true)); }
  finally { button.disabled = false; button.textContent = 'Выбрать торрент'; }
}

function renderTrackerResult(item, category = 'movies') {
  const card = element('article', 'result-card tracker-result');
  const body = element('div');
  body.append(element('span', 'source-badge tracker', category === 'music' ? 'RuTracker · музыка' : 'RuTracker'));
  body.append(element('h3', '', item.title));
  body.append(element('div', 'result-meta', `${item.forum || 'RuTracker'} · ${item.size || 'размер неизвестен'} · ${item.seeders} сидов · ${item.leechers} личей`));
  const actions = element('div', 'result-actions');
  const download = element('button', 'primary-button', 'Скачать');
  download.addEventListener('click', async () => {
    download.disabled = true;
    try {
      await api('/api/tracker/download', {
        method: 'POST', headers: {'Content-Type': 'application/json', 'X-HomeNode-Request': '1'},
        body: JSON.stringify({topic_id: item.topic_id, title: item.title, category}),
      });
      toast('Добавлено в загрузки');
      await loadDownloads(true);
    } catch (error) { toast(error.message); }
    finally { download.disabled = false; }
  });
  actions.append(download);
  body.append(actions);
  card.append(body);
  return card;
}

async function fetchLibrary() {
  const [library, progress] = await Promise.all([
    api('/api/library'),
    api('/api/watch-progress?limit=100'),
  ]);
  state.library = library;
  state.watchProgress = progress;
  state.watchProgressByPath = new Map(progress.map((item) => [item.path, item]));
  return state.library;
}

function progressForItem(item, includeCompleted = false) {
  const paths = new Set((item.files || []).map((file) => file.path));
  return state.watchProgress.find((progress) => paths.has(progress.path) && (includeCompleted || !progress.completed));
}

function resumableProgress(progress) {
  if (!progress || progress.completed || progress.position_seconds < 5) return false;
  return !progress.duration_seconds || progress.duration_seconds - progress.position_seconds > 15;
}

function nextFile(item, file) {
  const files = item?.files || [];
  const index = files.findIndex((candidate) => candidate.path === file?.path);
  return index >= 0 && index + 1 < files.length ? files[index + 1] : null;
}

function renderLibraryCard(item, compact = false) {
  const isMusic = item.category === 'music';
  const card = element('article', `${compact ? 'recent-card' : 'library-card'}${isMusic ? ' music-card' : ''}`);
  if (isMusic && !item.poster_url) {
    const cover = element('div', `${compact ? 'recent-poster' : 'library-poster'} music-cover`, '♪');
    cover.setAttribute('aria-label', `Обложка «${item.title}»`);
    card.append(cover);
  } else {
    card.append(poster(item.poster_url, item.title, compact ? 'recent-poster' : 'library-poster'));
  }
  const body = element('div', compact ? '' : 'library-card-body');
  body.append(element('h3', '', item.title));
  if (isMusic && item.artist) body.append(element('div', 'music-artist', item.artist));
  const unit = isMusic ? (item.file_count === 1 ? 'трек' : 'треков') : (item.file_count === 1 ? 'серия' : 'серий');
  body.append(element('div', 'meta', `${item.file_count} ${unit} · ${humanBytes(item.size)}`));
  const saved = !isMusic ? progressForItem(item) : null;
  if (resumableProgress(saved)) {
    const file = (item.files || []).find((candidate) => candidate.path === saved.path);
    body.append(element('div', 'resume-meta', `Продолжить: ${file?.name || 'последняя серия'} · ${playbackTime(saved.position_seconds)}`));
  }
  const actions = element('div', 'library-card-actions');
  const actionLabel = isMusic ? (item.file_count === 1 ? 'Слушать' : 'Открыть альбом')
    : resumableProgress(saved) ? 'Продолжить'
    : (item.file_count === 1 ? 'Смотреть' : 'Выбрать серию');
  const watch = element('button', compact ? 'text-button watch-button' : 'primary-button watch-button', actionLabel);
  watch.addEventListener('click', () => openPlayer(item, saved?.path));
  actions.append(watch);
  if (!compact) {
    const menu = element('button', 'menu-button');
    menu.title = 'Копировать SMB-путь';
    menu.append(icon('dots', 'Действия'));
    menu.addEventListener('click', async () => { await safeCopy(item.smb_path); toast('SMB-путь скопирован'); });
    actions.append(menu);
  }
  body.append(actions);
  card.append(body);
  return card;
}

function streamURL(file) {
  return `/api/media/stream?path=${encodeURIComponent(file.path)}`;
}

function compatibleStreamURL(file, start = 0, cached = false) {
  const suffix = start > 0 ? `&start=${encodeURIComponent(start.toFixed(3))}` : '';
  return `/api/media/compatible?path=${encodeURIComponent(file.path)}${suffix}${cached ? '&cache=1' : ''}`;
}

function needsCompatibleStream(file) {
  return isAppleWebKit && !/\.(mp4|m4v)$/i.test(file.name || file.path);
}

function updateLocalProgress(progress) {
  state.watchProgress = state.watchProgress.filter((item) => item.path !== progress.path);
  state.watchProgress.unshift(progress);
  state.watchProgressByPath.set(progress.path, progress);
}

function saveCurrentProgress(completed = false, force = false, keepalive = false) {
  const {playingItem: item, playingFile: file, playingPlayer: player} = state;
  if (!item || !file || !player || item.category === 'music') return;
  const now = Date.now();
  if (!force && now - state.lastProgressWrite < 10000) return;
  const localPosition = Number(player.currentTime);
  if (!Number.isFinite(localPosition)) return;
  const position = Math.max(0, state.playingOffset + localPosition);
  const known = state.watchProgressByPath.get(file.path);
  const playerDuration = Number(player.duration);
  const duration = Number.isFinite(playerDuration) && playerDuration > 0
    ? state.playingOffset + playerDuration
    : Number(known?.duration_seconds || 0);
  const isCompleted = completed || Boolean(known?.completed) || (duration > 0 && position / duration >= 0.95);
  if (position < 2 && !isCompleted) return;
  const payload = {
    path: file.path, title: item.title, position_seconds: position,
    duration_seconds: duration, completed: isCompleted,
  };
  updateLocalProgress({...payload, category: item.category, updated_at: new Date().toISOString()});
  state.lastProgressWrite = now;
  const options = {
    method: 'PUT', credentials: 'same-origin', keepalive,
    headers: {'Content-Type': 'application/json', 'X-HomeNode-Request': '1'},
    body: JSON.stringify(payload),
  };
  if (keepalive) fetch('/api/watch-progress', options).catch(() => {});
  else api('/api/watch-progress', options).catch(() => {});
}

function playFile(item, file, selectedButton, forceDirect = false, skipPreviousSave = false) {
  cancelAutoplayCountdown();
  const isMusic = item.category === 'music';
  const player = isMusic ? $('#audio-player') : $('#media-player');
  const otherPlayer = isMusic ? $('#media-player') : $('#audio-player');
  const compatible = !isMusic && needsCompatibleStream(file) && !forceDirect;
  const cachedCompatible = compatible && isTailscaleHTTPS;
  if (state.playingPlayer && !skipPreviousSave) {
    saveCurrentProgress(false, true);
    state.playingPlayer.pause();
  }
  const saved = state.watchProgressByPath.get(file.path);
  const resumeAt = resumableProgress(saved) ? Number(saved.position_seconds) : 0;
  state.playingItem = item;
  state.playingFile = file;
  state.playingButton = selectedButton;
  state.playingPlayer = player;
  state.playingOffset = compatible && !cachedCompatible ? resumeAt : 0;
  state.lastProgressWrite = 0;
  otherPlayer.pause();
  otherPlayer.removeAttribute('src');
  otherPlayer.hidden = true;
  player.hidden = false;
  player.src = compatible ? compatibleStreamURL(file, cachedCompatible ? 0 : resumeAt, cachedCompatible) : streamURL(file);
  player.dataset.mode = cachedCompatible ? 'compatible-cache' : compatible ? 'compatible' : 'direct';
  player.load();
  $('#player-title').textContent = item.file_count === 1 ? item.title : file.name;
  $('#player-note').textContent = isMusic
    ? 'Музыка передаётся с SSD без перекодирования. Для фонового воспроизведения на iPhone можно также открыть папку HomeNode через VLC.'
    : compatible
    ? `${resumeAt ? `Продолжаем с ${playbackTime(resumeAt)}. ` : ''}${cachedCompatible ? 'Подготавливаем надёжный MP4 для Tailscale; первый запуск серии обычно занимает 5–15 секунд. ' : 'Режим Safari: MKV без перекодирования упаковывается в MP4. Запуск может занять несколько секунд; '}Встроенные ASS-субтитры недоступны.`
    : 'Исходный файл передаётся напрямую. Если браузер не поддерживает его формат, используйте VLC и SMB-папку HomeNode.';
  $('#player-direct').hidden = !compatible;
  const upcoming = !isMusic ? nextFile(item, file) : null;
  $('#next-episode').hidden = !upcoming;
  $('#next-episode').title = upcoming ? `Открыть: ${upcoming.name}` : '';
  $$('.episode-button', $('#episode-list')).forEach((button) => button.classList.toggle('active', button === selectedButton));
  if ((!compatible || cachedCompatible) && resumeAt) {
    player.addEventListener('loadedmetadata', () => {
      if (state.playingFile?.path !== file.path) return;
      try {
        player.currentTime = Math.min(resumeAt, Math.max(0, player.duration - 1));
        toast(`Продолжаем с ${playbackTime(resumeAt)}`);
      } catch (_) { /* Формат может не поддерживать точную перемотку. */ }
    }, {once: true});
  }
  player.play().catch(() => {});
}

function openPlayer(item, preferredPath = '') {
  const dialog = $('#media-dialog');
  const list = $('#episode-list');
  const files = item.files || [];
  list.replaceChildren();
  $('#player-title').textContent = item.title;
  const isMusic = item.category === 'music';
  $('.player-heading .eyebrow').textContent = isMusic ? 'Локальная музыка' : 'Локальная медиатека';
  const buttons = new Map();
  files.forEach((file, index) => {
    const row = element('div', 'episode-row');
    const button = element('button', 'episode-button');
    const label = element('span');
    const saved = state.watchProgressByPath.get(file.path);
    const detail = saved?.completed ? `${humanBytes(file.size)} · просмотрено`
      : resumableProgress(saved) ? `${humanBytes(file.size)} · ${playbackTime(saved.position_seconds)}`
      : humanBytes(file.size);
    label.append(element('strong', '', file.name), element('small', '', detail));
    button.append(element('span', 'episode-number', String(index + 1)), label);
    button.dataset.path = file.path;
    button.addEventListener('click', () => playFile(item, file, button));
    const watched = element('button', `watched-toggle${saved?.completed ? ' watched' : ''}`, saved?.completed ? '✓' : '○');
    watched.type = 'button';
    watched.title = saved?.completed ? 'Отметить непросмотренным' : 'Отметить просмотренным';
    watched.setAttribute('aria-label', watched.title);
    watched.addEventListener('click', async () => {
      await setWatchedStatus(item, file, !state.watchProgressByPath.get(file.path)?.completed);
      const current = state.watchProgressByPath.get(file.path);
      watched.classList.toggle('watched', Boolean(current?.completed));
      watched.textContent = current?.completed ? '✓' : '○';
      watched.title = current?.completed ? 'Отметить непросмотренным' : 'Отметить просмотренным';
      watched.setAttribute('aria-label', watched.title);
      $('small', button).textContent = current?.completed ? `${humanBytes(file.size)} · просмотрено`
        : resumableProgress(current) ? `${humanBytes(file.size)} · ${playbackTime(current.position_seconds)}` : humanBytes(file.size);
    });
    row.append(button, watched);
    list.append(row);
    buttons.set(file.path, button);
  });
  dialog.showModal();
  const recent = progressForItem(item);
  const selected = files.find((file) => file.path === preferredPath)
    || files.find((file) => file.path === recent?.path)
    || files[0];
  if (selected) playFile(item, selected, buttons.get(selected.path));
}

function playNextEpisode() {
  const item = state.playingItem;
  const upcoming = nextFile(item, state.playingFile);
  if (!item || !upcoming) return;
  saveCurrentProgress(true, true);
  const button = $$('.episode-button', $('#episode-list')).find((candidate) => candidate.dataset.path === upcoming.path);
  playFile(item, upcoming, button, false, true);
  button?.scrollIntoView({block: 'nearest', behavior: 'smooth'});
}

function cancelAutoplayCountdown() {
  if (state.autoplayTimer) window.clearInterval(state.autoplayTimer);
  state.autoplayTimer = null;
  state.autoplayRemaining = 0;
  const notice = $('#autoplay-countdown');
  if (notice) notice.hidden = true;
}

function startAutoplayCountdown() {
  cancelAutoplayCountdown();
  if (!nextFile(state.playingItem, state.playingFile)) return;
  state.autoplayRemaining = 8;
  $('#autoplay-seconds').textContent = String(state.autoplayRemaining);
  $('#autoplay-countdown').hidden = false;
  state.autoplayTimer = window.setInterval(() => {
    state.autoplayRemaining -= 1;
    $('#autoplay-seconds').textContent = String(Math.max(0, state.autoplayRemaining));
    if (state.autoplayRemaining <= 0) {
      cancelAutoplayCountdown();
      playNextEpisode();
    }
  }, 1000);
}

async function setWatchedStatus(item, file, completed) {
  const known = state.watchProgressByPath.get(file.path);
  const payload = {
    path: file.path, title: item.title,
    position_seconds: completed ? Number(known?.duration_seconds || known?.position_seconds || 0) : 0,
    duration_seconds: Number(known?.duration_seconds || 0), completed,
  };
  await api('/api/watch-progress', {
    method: 'PUT', headers: {'Content-Type': 'application/json', 'X-HomeNode-Request': '1'}, body: JSON.stringify(payload),
  });
  updateLocalProgress({...payload, category: item.category, updated_at: new Date().toISOString()});
  toast(completed ? 'Серия отмечена просмотренной' : 'Отметка просмотра снята');
  renderContinueWatching();
  renderLibrary();
}

$('#next-episode').addEventListener('click', playNextEpisode);
$('#cancel-autoplay').addEventListener('click', cancelAutoplayCountdown);

$('#player-direct').addEventListener('click', () => {
  if (state.playingItem && state.playingFile) {
    playFile(state.playingItem, state.playingFile, state.playingButton, true);
  }
});

$('#media-player').addEventListener('error', () => {
  const compatible = $('#media-player').dataset.mode?.startsWith('compatible');
  $('#player-note').textContent = compatible
    ? 'Safari не смог воспроизвести кодек этого файла. Нажмите «Открыть исходный файл» или откройте папку HomeNode через VLC.'
    : 'Браузер не поддерживает этот контейнер или кодек. Откройте папку HomeNode через VLC по SMB.';
});

$('#audio-player').addEventListener('error', () => {
  $('#player-note').textContent = 'Браузер не поддерживает формат этого трека. Откройте папку HomeNode через VLC.';
});

$('#media-player').addEventListener('timeupdate', () => saveCurrentProgress());
$('#media-player').addEventListener('pause', () => saveCurrentProgress(false, true));
$('#media-player').addEventListener('ended', () => {
  saveCurrentProgress(true, true);
  renderContinueWatching();
  renderLibrary();
  startAutoplayCountdown();
});

function closePlayer() {
  cancelAutoplayCountdown();
  saveCurrentProgress(false, true);
  [$('#media-player'), $('#audio-player')].forEach((player) => {
    player.pause();
    player.removeAttribute('src');
    player.load();
  });
  state.playingItem = null;
  state.playingFile = null;
  state.playingButton = null;
  state.playingPlayer = null;
  state.playingOffset = 0;
  $('#next-episode').hidden = true;
  $('#media-dialog').close();
  renderContinueWatching();
  renderLibrary();
}

$('#close-player').addEventListener('click', closePlayer);
$('#media-dialog').addEventListener('click', (event) => { if (event.target === $('#media-dialog')) closePlayer(); });
window.addEventListener('pagehide', () => saveCurrentProgress(false, true, true));
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'hidden') saveCurrentProgress(false, true, true);
});

function renderContinueWatching() {
  const section = $('#continue-section');
  const container = $('#continue-watching');
  if (!section || !container) return;
  const seenTitles = new Set();
  const entries = state.watchProgress.map((progress) => {
    const item = state.library.find((candidate) => (candidate.files || []).some((file) => file.path === progress.path));
    const file = item?.files?.find((candidate) => candidate.path === progress.path);
    const key = item?.folder || item?.title;
    if (!item || !file || progress.category === 'music' || seenTitles.has(key)) return null;
    seenTitles.add(key);
    return {progress, item, file};
  }).filter(Boolean).slice(0, 6);
  section.hidden = entries.length === 0;
  container.replaceChildren();
  if (!entries.length) return;
  $('#last-watched-note').textContent = `Последнее: ${entries[0].item.title}`;
  entries.forEach(({progress, item, file}) => {
    const card = element('article', 'continue-card');
    card.append(poster(item.poster_url, item.title, 'continue-poster'));
    const body = element('div', 'continue-card-body');
    body.append(element('h3', '', item.title));
    body.append(element('div', 'meta continue-file', file.name));
    const percent = progress.duration_seconds > 0
      ? Math.round(Math.min(1, progress.position_seconds / progress.duration_seconds) * 100) : 0;
    const bar = element('div', 'progress watch-progress');
    const fill = element('span');
    fill.style.width = `${percent}%`;
    bar.append(fill);
    body.append(bar);
    const detail = progress.completed ? 'Просмотрено' : `${playbackTime(progress.position_seconds)}${progress.duration_seconds ? ` из ${playbackTime(progress.duration_seconds)}` : ''}`;
    body.append(element('div', 'continue-detail', detail));
    const actions = element('div', 'continue-actions');
    const action = element('button', 'text-button', progress.completed ? 'Посмотреть снова' : 'Продолжить');
    action.addEventListener('click', () => openPlayer(item, file.path));
    actions.append(action);
    const upcoming = nextFile(item, file);
    if (upcoming) {
      const next = element('button', 'text-button next-episode-card', 'Следующая серия');
      next.title = upcoming.name;
      next.addEventListener('click', () => openPlayer(item, upcoming.path));
      actions.append(next);
    }
    body.append(actions);
    card.append(body);
    container.append(card);
  });
}

function renderLibrary() {
  const container = $('#library-results');
  const query = $('#library-query').value.trim().toLowerCase();
  const filtered = state.library.filter((item) => (state.libraryCategory === 'all' || item.category === state.libraryCategory) && item.title.toLowerCase().includes(query));
  container.replaceChildren();
  if (!filtered.length) return container.append(emptyState('Здесь пока пусто', 'Измените фильтр или добавьте новый тайтл.'));
  filtered.forEach((item) => container.append(renderLibraryCard(item)));
}

async function loadLibrary(silent = false) {
  const container = $('#library-results');
  if (!silent) container.replaceChildren(emptyState('Читаем медиатеку…'));
  try { await fetchLibrary(); renderContinueWatching(); renderLibrary(); }
  catch (error) { container.replaceChildren(emptyState('Не удалось прочитать медиатеку', error.message, true)); }
}

async function loadRecentLibrary() {
  const container = $('#recent-library');
  try {
    if (!state.library.length) await fetchLibrary();
    container.replaceChildren();
    const items = state.library.slice(0, 4);
    if (!items.length) return container.append(emptyState('Медиатека пока пуста'));
    items.forEach((item) => container.append(renderLibraryCard(item, true)));
  } catch (error) { container.replaceChildren(emptyState('Медиатека недоступна', error.message, true)); }
}

$$('[data-library-category]').forEach((button) => button.addEventListener('click', () => {
  state.libraryCategory = button.dataset.libraryCategory;
  $$('[data-library-category]').forEach((item) => item.classList.toggle('active', item === button));
  renderLibrary();
}));
$('#library-query').addEventListener('input', renderLibrary);
$('#refresh-library').addEventListener('click', () => loadLibrary());

const downloadStatus = {
  0: 'На паузе', 1: 'Ожидает проверки', 2: 'Проверяется', 3: 'В очереди', 4: 'Загружается', 5: 'Ожидает раздачи', 6: 'Готово · раздаётся',
};

const downloadMatches = (item) => {
  if (state.downloadFilter === 'active') return item.status === 4 || item.status === 3;
  if (state.downloadFilter === 'done') return (item.percentDone || 0) >= 1;
  if (state.downloadFilter === 'paused') return item.status === 0;
  return true;
};

function renderDownloads() {
  const container = $('#download-results');
  const items = state.downloads.filter(downloadMatches);
  container.replaceChildren();
  if (!items.length) container.append(emptyState('Нет загрузок в этом разделе'));
  items.forEach((item) => {
    const row = element('button', `download-row${item.id === state.selectedDownloadID ? ' selected' : ''}`);
    row.type = 'button';
    row.append(element('span', 'download-row-icon', percentage(item) >= 100 ? '✓' : '↓'));
    const body = element('div');
    body.append(element('h3', '', item.name));
    body.append(element('div', 'meta', `${downloadStatus[item.status] || 'Неизвестно'} · ${percentage(item)}% · ${humanBytes(item.totalSize)}`));
    const progress = element('div', 'progress');
    const bar = element('span');
    bar.style.width = `${percentage(item)}%`;
    progress.append(bar);
    body.append(progress);
    row.append(body);
    row.append(element('span', 'download-rate', item.rateDownload ? humanRate(item.rateDownload) : (item.rateUpload ? `↑ ${humanRate(item.rateUpload)}` : '')));
    row.addEventListener('click', () => { state.selectedDownloadID = item.id; renderDownloads(); renderDownloadInspector(item); });
    container.append(row);
  });
  const selected = state.downloads.find((item) => item.id === state.selectedDownloadID);
  if (selected) renderDownloadInspector(selected);
}

function relatedPoster(item) {
  const torrentName = item.name.toLowerCase();
  return state.library.find((entry) => torrentName.includes(entry.title.toLowerCase()) || entry.title.toLowerCase().includes(torrentName))?.poster_url || '';
}

function renderDownloadInspector(item) {
  const inspector = $('#download-inspector');
  inspector.classList.add('has-selection');
  inspector.replaceChildren();
  inspector.append(poster(relatedPoster(item), item.name, 'inspector-poster'));
  inspector.append(element('p', 'eyebrow', 'Выбранная загрузка'));
  inspector.append(element('h2', '', item.name));
  inspector.append(element('div', 'meta', `${downloadStatus[item.status] || 'Неизвестно'} · ${percentage(item)}%`));
  const progress = element('div', 'progress');
  const bar = element('span'); bar.style.width = `${percentage(item)}%`; progress.append(bar); inspector.append(progress);
  const stats = element('div', 'inspector-stats');
  [['Размер', humanBytes(item.totalSize)], ['Осталось', humanBytes(item.leftUntilDone)], ['Скачивание', humanRate(item.rateDownload)], ['Отдача', humanRate(item.rateUpload)]].forEach(([label, value]) => {
    const stat = element('div', 'stat'); stat.append(element('span', '', label), element('strong', '', value)); stats.append(stat);
  });
  inspector.append(stats);
  if (item.errorString) inspector.append(element('p', 'inline-message', item.errorString));
  const actions = element('div', 'inspector-actions');
  const toggle = element('button', 'secondary-button', item.status === 0 ? 'Продолжить загрузку' : 'Поставить на паузу');
  toggle.addEventListener('click', async () => {
    toggle.disabled = true;
    try {
      await api(`/api/downloads/${item.id}/${item.status === 0 ? 'start' : 'stop'}`, {method: 'POST', headers: {'X-HomeNode-Request': '1'}});
      await loadDownloads(true);
    } catch (error) { toast(error.message); toggle.disabled = false; }
  });
  const remove = element('button', 'secondary-button', 'Убрать задачу, оставить видео');
  remove.addEventListener('click', async () => {
    if (!window.confirm('Убрать задачу из списка? Видео останется на SSD и в медиатеке.')) return;
    await removeDownload(item, false);
  });
  const removeData = element('button', 'danger-button', 'Удалить торрент и видео');
  removeData.addEventListener('click', async () => {
    const confirmation = window.prompt(`Это безвозвратно удалит задачу и файлы «${item.name}» с SSD.\n\nВведите УДАЛИТЬ для подтверждения:`);
    if (confirmation !== 'УДАЛИТЬ') { if (confirmation !== null) toast('Удаление отменено: слово не совпало'); return; }
    await removeDownload(item, true);
  });
  actions.append(toggle, remove, removeData);
  inspector.append(actions);
  inspector.append(element('p', 'delete-explanation', '«Убрать задачу» сохраняет видео. Красная кнопка удаляет и торрент, и его файлы с SSD.'));
  if (window.matchMedia('(max-width: 820px)').matches) {
    window.requestAnimationFrame(() => inspector.scrollIntoView({behavior: 'smooth', block: 'start'}));
  }
}

async function removeDownload(item, deleteData) {
  try {
    await api(`/api/downloads/${item.id}${deleteData ? '/data' : ''}`, {method: 'DELETE', headers: {'X-HomeNode-Request': '1'}});
    state.selectedDownloadID = null;
    toast(deleteData ? 'Торрент и файлы удалены' : 'Задача убрана, видео сохранено');
    $('#download-inspector').classList.remove('has-selection');
    $('#download-inspector').replaceChildren(emptyState('Выберите загрузку', 'Здесь появятся детали и управление.'));
    await Promise.all([loadDownloads(true), loadLibrary(true)]);
  } catch (error) { toast(error.message); }
}

async function loadDownloads(silent = false) {
  const container = $('#download-results');
  if (!silent) container.replaceChildren(emptyState('Читаем очередь…'));
  try {
    if (!state.library.length) await fetchLibrary().catch(() => []);
    state.downloads = await api('/api/downloads');
    const active = state.downloads.filter((item) => item.status === 3 || item.status === 4).length;
    const count = $('#download-count'); count.hidden = active === 0; count.textContent = String(active);
    renderDownloads();
  } catch (error) { container.replaceChildren(emptyState('Transmission недоступен', error.message, true)); }
}

$$('[data-download-filter]').forEach((button) => button.addEventListener('click', () => {
  state.downloadFilter = button.dataset.downloadFilter;
  $$('[data-download-filter]').forEach((item) => item.classList.toggle('active', item === button));
  renderDownloads();
}));
$('#refresh-downloads').addEventListener('click', () => loadDownloads());

async function loadHistory() {
  const container = $('#history-results');
  container.replaceChildren(emptyState('Читаем историю…'));
  try {
    const items = await api('/api/history');
    container.replaceChildren();
    if (!items.length) return container.append(emptyState('История пока пуста'));
    items.forEach((item) => {
      const row = element('article', 'timeline-item');
      row.append(element('span', 'timeline-dot'));
      const body = element('div');
      body.append(element('p', '', item.title || actionLabel(item.action)));
      body.append(element('div', 'meta', `${sourceLabel(item.source)} · ${actionLabel(item.action)} · ${new Date(item.created_at).toLocaleString('ru-RU')}`));
      row.append(body); container.append(row);
    });
  } catch (error) { container.replaceChildren(emptyState('Не удалось прочитать историю', error.message, true)); }
}

function actionLabel(action) {
  return ({search: 'Поиск', 'catalog-search': 'Поиск с фильтрами', download: 'Добавлено в загрузки', start: 'Загрузка продолжена', stop: 'Загрузка остановлена', remove: 'Задача убрана', 'remove-data': 'Удалены задача и файлы'})[action] || action;
}
function sourceLabel(source) { return ({aniliberty: 'AniLiberty', rutracker: 'RuTracker', transmission: 'Transmission'})[source] || source; }
$('#refresh-history').addEventListener('click', loadHistory);

async function loadDevices() {
  const container = $('#device-results');
  container.replaceChildren(emptyState('Ищем устройства…'));
  try {
    const [devices, health] = await Promise.all([api('/api/devices'), api('/api/system/health')]);
    renderSystemHealth(health);
    container.replaceChildren();
    if (!devices.length) return container.append(emptyState('Устройства пока не найдены', 'Подключите устройство к Wi‑Fi или Ethernet и обновите список.'));
    devices.forEach((device) => {
      const mode = device.vpn_mode || (device.vpn ? 'full' : 'direct');
      const row = element('article', `device-card${mode !== 'direct' ? ' vpn-active' : ''}`);
      const badge = element('div', 'device-avatar', (device.name || '?').slice(0, 1).toUpperCase());
      const info = element('div', 'device-info');
      info.append(element('h3', '', device.name));
      info.append(element('div', 'meta', `${device.ip || 'IP пока неизвестен'} · ${device.mac}`));
      const stateLabel = element('span', `device-state${device.online ? ' online' : ''}`, device.online ? 'В сети' : 'Неактивно');
      info.append(stateLabel);
      const controls = element('div', 'device-routing-controls');
      const modes = [
        ['direct', 'Обычно'],
        ['smart', 'Только сервисы'],
        ['full', 'Весь VPN'],
      ];
      modes.forEach(([value, label]) => {
        const button = element('button', `${value === mode ? 'primary-button' : 'secondary-button'} device-route-button`, label);
        button.setAttribute('aria-pressed', value === mode ? 'true' : 'false');
        button.addEventListener('click', async () => {
          if (value === mode) return;
          controls.querySelectorAll('button').forEach((item) => { item.disabled = true; });
          try {
            const options = value === 'direct'
              ? {method: 'DELETE', headers: {'X-HomeNode-Request': '1'}}
              : {method: 'POST', headers: {'Content-Type': 'application/json', 'X-HomeNode-Request': '1'}, body: JSON.stringify({mode: value})};
            await api(`/api/devices/${encodeURIComponent(device.mac)}/vpn`, options);
            toast(value === 'direct' ? 'Устройство использует обычный интернет' : value === 'smart' ? 'VPN включён только для сервисов' : 'Весь трафик устройства направлен через VPN');
            await loadDevices();
          } catch (error) {
            toast(error.message);
            controls.querySelectorAll('button').forEach((item) => { item.disabled = false; });
          }
        });
        controls.append(button);
      });
      row.append(badge, info, controls); container.append(row);
    });
  } catch (error) { container.replaceChildren(emptyState('Не удалось загрузить устройства', error.message, true)); }
}
$('#refresh-devices').addEventListener('click', loadDevices);

function renderSystemHealth(health) {
  const container = $('#system-health');
  const vpn = health.vpn || {};
  const ssd = health.ssd || {};
  const backup = health.backup || {};
  const cards = [
    ['VPN', vpn.status === 'ok' ? 'Стабильно' : vpn.status === 'degraded' ? 'Есть задержки' : vpn.status === 'down' ? 'Нет связи' : 'Нет данных', `${Math.round(vpn.latency_ms || 0)} мс · потери ${vpn.packet_loss || 0}% · handshake ${vpn.handshake_age_seconds || 0} с`, vpn.status !== 'ok'],
    ['SSD', ssd.status === 'passed' ? 'SMART пройден' : 'Нужна проверка', `${ssd.temperature_c || 0} °C · переназначено блоков: ${ssd.reallocated_sectors || 0} · свободно ${humanBytes(health.disk_free)}`, ssd.status !== 'passed' || Number(ssd.reallocated_sectors) > 0],
    ['Резервная копия', backup.status === 'ok' ? 'Создана' : 'Ожидается', backup.created_at ? `${new Date(backup.created_at).toLocaleString('ru-RU')} · ${humanBytes(backup.size)}` : 'Будет создана автоматически', backup.status !== 'ok'],
    ['База данных', health.database_integrity === 'ok' ? 'Исправна' : 'Ошибка проверки', 'Автокопия раз в сутки · хранится 7 последних', health.database_integrity !== 'ok'],
  ];
  container.replaceChildren();
  cards.forEach(([title, value, detail, warning]) => {
    const card = element('article', `health-card${warning ? ' warning' : ''}`);
    card.append(element('span', 'health-label', title), element('strong', '', value), element('small', '', detail));
    container.append(card);
  });
}

async function loadTelegram() {
  const container = $('#telegram-card');
  container.replaceChildren(emptyState('Проверяем Telegram…'));
  try {
    const status = await api('/api/telegram/status');
    container.replaceChildren();
    const header = element('div', 'integration-status');
    header.append(element('span', 'brand-mark', 'T'));
    const info = element('div');
    info.append(element('h2', '', status.paired ? 'Telegram подключён' : 'Подключение Telegram'));
    const detail = !status.configured ? 'Токен бота не настроен.'
      : status.paired ? `Владелец: ${status.owner_username ? `@${status.owner_username}` : 'привязанное устройство'}`
      : status.connected ? `Бот @${status.bot_username} готов к привязке.` : 'Бот настроен, но Telegram пока недоступен.';
    info.append(element('p', 'lead', detail));
    header.append(info); container.append(header);
    const actions = element('div', 'integration-actions');
    if (status.paired) {
      const open = element('a', 'primary-button', 'Открыть бота');
      open.href = `https://t.me/${status.bot_username}`; open.target = '_blank'; open.rel = 'noreferrer';
      actions.append(open);
      const test = element('button', 'secondary-button', 'Отправить тест');
      test.addEventListener('click', async () => {
        test.disabled = true;
        try {
          await api('/api/telegram/test', {method: 'POST', headers: {'X-HomeNode-Request': '1'}});
          toast('Тестовое сообщение отправлено');
        } catch (error) { toast(error.message); }
        finally { test.disabled = false; }
      });
      actions.append(test);
      const unpair = element('button', 'danger-button', 'Отключить владельца');
      unpair.addEventListener('click', async () => {
        if (!window.confirm('Отключить Telegram-владельца? Бот перестанет выполнять его команды.')) return;
        await api('/api/telegram/pairing', {method: 'DELETE', headers: {'X-HomeNode-Request': '1'}});
        toast('Telegram отключён'); loadTelegram();
      });
      actions.append(unpair);
    } else if (status.configured) {
      const pair = element('button', 'primary-button', 'Получить код привязки');
      pair.disabled = !status.connected;
      pair.addEventListener('click', async () => {
        pair.disabled = true;
        try {
          const result = await api('/api/telegram/pairing', {method: 'POST', headers: {'X-HomeNode-Request': '1'}});
          const block = element('div', 'pairing-code');
          block.append(element('strong', '', `Отправьте боту @${status.bot_username}:`));
          block.append(element('code', '', `/pair ${result.code}`));
          block.append(element('p', '', 'Код действует 10 минут и принимает не более пяти попыток. После отправки обновите эту страницу.'));
          const open = element('a', 'primary-button', 'Открыть Telegram');
          open.href = `https://t.me/${status.bot_username}`; open.target = '_blank'; open.rel = 'noreferrer';
          block.append(open); container.append(block);
        } catch (error) { toast(error.message); pair.disabled = false; }
      });
      actions.append(pair);
    }
    container.append(actions);
  } catch (error) { container.replaceChildren(emptyState('Telegram недоступен', error.message, true)); }
}
$('#refresh-telegram').addEventListener('click', loadTelegram);

$('#logout').addEventListener('click', async () => {
  await api('/api/session/logout', {method: 'POST', headers: {'X-HomeNode-Request': '1'}});
  window.location.replace('/login');
});

async function loadStatus() {
  try {
    const status = await api('/api/status');
    const disk = $('#disk-status');
    $('.status-dot', disk).classList.add('ok');
    $('span:last-child', disk).textContent = `Свободно ${humanBytes(status.disk_free)}`;
    $('.status-dot', $('#mobile-status')).classList.add('ok');
  } catch (error) { $('span:last-child', $('#disk-status')).textContent = error.message; }
}

const initialView = ['search', 'library', 'downloads', 'history', 'devices', 'telegram'].includes(location.hash.slice(1)) ? location.hash.slice(1) : 'search';
setView(initialView);
loadStatus();
loadRecentLibrary();
loadDownloads(true);
if (isAppleWebKit && 'serviceWorker' in navigator && location.protocol === 'https:') {
  navigator.serviceWorker.register('/sw.js?v=20260930-4').catch(() => {});
}
if (isAppleWebKit && !navigator.standalone && !localStorage.getItem('homenode-ios-install-dismissed')) {
  $('#ios-install-hint').hidden = false;
}
$('#dismiss-ios-install').addEventListener('click', () => {
  localStorage.setItem('homenode-ios-install-dismissed', '1');
  $('#ios-install-hint').hidden = true;
});
window.setInterval(() => {
  if ($('#view-downloads').classList.contains('active')) loadDownloads(true);
}, 5000);
