(() => {
  const system = matchMedia('(prefers-color-scheme: light)');
  const valid = value => ['auto', 'light', 'dark'].includes(value);
  let mode = 'auto';
  try { const saved = localStorage.getItem('kube-res-foresight-theme'); if (valid(saved)) mode = saved; } catch { /* Use the system setting. */ }
  const apply = () => {
    const light = mode === 'light' || (mode === 'auto' && system.matches);
    document.getElementById('light-theme').media = mode === 'auto' ? '(prefers-color-scheme: light)' : light ? 'all' : 'not all';
    document.documentElement.dataset.theme = light ? 'light' : 'dark';
    document.documentElement.dataset.themeMode = mode;
    window.dispatchEvent(new Event('kube-res-foresight-themechange'));
  };
  window.KUBE_RES_FORESIGHT_THEME = Object.freeze({
    getMode: () => mode,
    setMode: value => {
      if (!valid(value)) return;
      mode = value;
      try { localStorage.setItem('kube-res-foresight-theme', mode); } catch { /* Still applies to the current page. */ }
      apply();
    }
  });
  system.addEventListener('change', () => { if (mode === 'auto') apply(); });
  window.addEventListener('storage', event => {
    if (event.key === 'kube-res-foresight-theme') { mode = valid(event.newValue) ? event.newValue : 'auto'; apply(); }
  });
  apply();
})();
