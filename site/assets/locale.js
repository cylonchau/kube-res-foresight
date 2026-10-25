// Runs before styles to choose the language without rendering the wrong page first.
(() => {
  const root = new URL(document.documentElement.dataset.siteRoot || './', location.href);
  const englishPage = document.documentElement.lang === 'en';
  const requested = new URL(location.href).searchParams.get('lang');
  let saved;
  try { saved = localStorage.getItem('kube-resource-fcst-language'); } catch { /* Storage may be disabled. */ }
  const browserLanguage = (navigator.languages?.[0] || navigator.language || 'en').toLowerCase();
  const valid = language => language === 'zh' || language === 'en';
  const language = valid(requested) ? requested : valid(saved) ? saved : englishPage ? 'en' : browserLanguage.startsWith('zh') ? 'zh' : 'en';
  if (valid(requested)) {
    try { localStorage.setItem('kube-resource-fcst-language', language); } catch { /* URL still preserves the choice. */ }
  }
  if ((language === 'en') !== englishPage) {
    const page = location.pathname.endsWith('docs.html') ? 'docs.html' : 'index.html';
    const destination = new URL(`${language === 'en' ? 'en/' : ''}${page}`, root);
    destination.search = location.search;
    destination.hash = location.hash;
    location.replace(destination.href);
  }
})();
