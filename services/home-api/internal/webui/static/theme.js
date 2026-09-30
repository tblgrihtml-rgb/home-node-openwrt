(() => {
  const storageKey = 'homenode-theme';
  const media = window.matchMedia('(prefers-color-scheme: dark)');

  const storedTheme = () => {
    try {
      const value = localStorage.getItem(storageKey);
      return value === 'dark' || value === 'light' ? value : '';
    } catch (_) { return ''; }
  };

  const updateControls = (theme) => {
    document.querySelectorAll('[data-theme-toggle]').forEach((button) => {
      const dark = theme === 'dark';
      button.setAttribute('aria-pressed', String(dark));
      button.setAttribute('aria-label', dark ? 'Включить светлую тему' : 'Включить тёмную тему');
      button.title = dark ? 'Светлая тема' : 'Тёмная тема';
      const glyph = button.querySelector('[data-theme-glyph]');
      const label = button.querySelector('[data-theme-label]');
      if (glyph) glyph.textContent = dark ? '☀' : '☾';
      if (label) label.textContent = dark ? 'Светлая тема' : 'Тёмная тема';
    });
  };

  const applyTheme = (theme, persist = false) => {
    document.documentElement.dataset.theme = theme;
    document.documentElement.style.colorScheme = theme;
    document.querySelector('meta[name="theme-color"]')?.setAttribute('content', theme === 'dark' ? '#100e16' : '#f7f5fa');
    if (persist) {
      try { localStorage.setItem(storageKey, theme); } catch (_) { /* Хранилище может быть отключено. */ }
    }
    updateControls(theme);
  };

  applyTheme(storedTheme() || (media.matches ? 'dark' : 'light'));

  window.addEventListener('DOMContentLoaded', () => {
    updateControls(document.documentElement.dataset.theme);
    document.querySelectorAll('[data-theme-toggle]').forEach((button) => {
      button.addEventListener('click', () => {
        applyTheme(document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark', true);
      });
    });
  });

  media.addEventListener('change', (event) => {
    if (!storedTheme()) applyTheme(event.matches ? 'dark' : 'light');
  });
})();
