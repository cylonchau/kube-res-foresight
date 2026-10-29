(() => {
  document.documentElement.classList.add('js');
  const root = new URL(document.documentElement.dataset.siteRoot || './', location.href);
  const page = document.documentElement.dataset.pagePath || 'index.html';
  const isEnglish = document.documentElement.lang === 'en';
  const requested = new URL(location.href).searchParams.get('lang');
  let saved;
  try { saved = localStorage.getItem('kube-resource-fcst-language'); } catch { /* Preference storage is optional. */ }
  const valid = value => ['zh', 'en'].includes(value);
  const browser = (navigator.languages?.[0] || navigator.language || 'en').toLowerCase();
  const language = valid(requested) ? requested : valid(saved) ? saved : isEnglish ? 'en' : browser.startsWith('zh') ? 'zh' : 'en';
  if (valid(requested)) {
    try { localStorage.setItem('kube-resource-fcst-language', language); } catch { /* The URL retains the choice. */ }
  }
  if ((language === 'en') !== isEnglish) {
    const target = new URL((language === 'en' ? 'en/' : '') + page, root);
    target.search = location.search;
    target.hash = location.hash;
    location.replace(target.href);
  }
})();
