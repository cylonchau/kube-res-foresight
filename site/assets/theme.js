// Apply the saved appearance before the page is painted. CSS handles System without JS too.
(() => {
  const system = matchMedia('(prefers-color-scheme: light)');
  const valid = mode => ['auto', 'light', 'dark'].includes(mode);
  let mode = 'auto';
  try { const saved = localStorage.getItem('kube-resource-fcst-theme'); if (valid(saved)) mode = saved; } catch { /* Use System. */ }
  const apply = () => {
    const light = mode === 'light' || (mode === 'auto' && system.matches);
    const stylesheet = document.getElementById('light-theme');
    stylesheet.media = mode === 'auto' ? '(prefers-color-scheme: light)' : light ? 'all' : 'not all';
    document.documentElement.dataset.theme = light ? 'light' : 'dark';
    document.documentElement.dataset.themeMode = mode;
    document.querySelectorAll('meta[name="theme-color"]').forEach(meta => { meta.content = light ? '#f6f9f8' : '#0b1116'; });
    window.dispatchEvent(new Event('kube-resource-fcst-themechange'));
  };
  window.KUBE_RESOURCE_FCST_THEME = Object.freeze({
    getMode: () => mode,
    setMode: value => {
      if (!valid(value)) return;
      mode = value;
      try { localStorage.setItem('kube-resource-fcst-theme', mode); } catch { /* Still works for this page. */ }
      apply();
    }
  });
  system.addEventListener('change', () => { if (mode === 'auto') apply(); });
  window.addEventListener('storage', event => {
    if (event.key === 'kube-resource-fcst-theme') { mode = valid(event.newValue) ? event.newValue : 'auto'; apply(); }
  });
  apply();
})();
