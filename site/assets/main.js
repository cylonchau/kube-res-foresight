'use strict';
const english = document.documentElement.lang === 'en';
const lang = english ? 'en' : 'zh';
const text = (zh, en) => english ? en : zh;
const root = new URL(document.documentElement.dataset.siteRoot || './', location.href);
const currentPage = document.documentElement.dataset.pagePath || 'index.html';
const localUrl = path => {
  const url = new URL(path, root);
  url.searchParams.set('lang', lang);
  return url;
};
const config = window.KUBE_RESOURCE_FCST_SITE;
if (config) {
  document.querySelectorAll('[data-repo]').forEach(link => { link.href = config.repository; });
  document.querySelectorAll('[data-issues]').forEach(link => { link.href = `${config.repository}/issues`; });
  document.querySelectorAll('[data-source]').forEach(link => { link.href = `${config.repository}/blob/${encodeURIComponent(config.branch)}/${link.dataset.source}`; });
}
document.querySelectorAll('[data-language]').forEach(link => {
  const code = link.dataset.language;
  const target = new URL((code === 'en' ? 'en/' : '') + currentPage, root);
  target.searchParams.set('lang', code);
  target.hash = location.hash;
  link.href = target.href;
  link.addEventListener('click', () => {
    const destination = new URL(link.href);
    destination.hash = location.hash;
    link.href = destination.href;
    try { localStorage.setItem('kube-resource-fcst-language', code); } catch { /* Retained in the URL. */ }
  });
});
document.querySelectorAll('a[href]').forEach(link => {
  if (link.getAttribute('href').startsWith('#') || link.hasAttribute('data-language') || link.hasAttribute('data-source')) return;
  const target = new URL(link.href);
  if (target.origin === location.origin && target.pathname.startsWith(root.pathname)) {
    target.searchParams.set('lang', lang);
    link.href = target.href;
  }
});

const menu = document.querySelector('.menu-toggle');
const nav = document.getElementById('site-nav');
const closeNav = () => { menu?.setAttribute('aria-expanded', 'false'); nav?.classList.remove('is-open'); };
menu?.addEventListener('click', () => {
  const open = menu.getAttribute('aria-expanded') !== 'true';
  menu.setAttribute('aria-expanded', String(open)); nav.classList.toggle('is-open', open);
});
nav?.querySelectorAll('a').forEach(link => link.addEventListener('click', closeNav));
const sidebarButton = document.querySelector('.docs-nav-toggle');
const backdrop = document.querySelector('.sidebar-backdrop');
const closeSidebar = () => {
  document.documentElement.classList.remove('sidebar-open');
  sidebarButton?.setAttribute('aria-expanded', 'false');
  if (backdrop) backdrop.hidden = true;
};
sidebarButton?.addEventListener('click', () => {
  const open = sidebarButton.getAttribute('aria-expanded') !== 'true';
  document.documentElement.classList.toggle('sidebar-open', open);
  sidebarButton.setAttribute('aria-expanded', String(open)); backdrop.hidden = !open;
});
backdrop?.addEventListener('click', closeSidebar);

const themeButton = document.getElementById('theme-toggle');
const themeMenu = document.getElementById('theme-menu');
const closeTheme = () => { if (themeMenu) themeMenu.hidden = true; themeButton?.setAttribute('aria-expanded', 'false'); };
const refreshTheme = () => {
  const mode = window.KUBE_RESOURCE_FCST_THEME?.getMode() || 'auto';
  const names = { auto: text('跟随系统', 'System'), light: text('浅色', 'Light'), dark: text('深色', 'Dark') };
  if (themeButton) {
    themeButton.querySelector('span').textContent = { auto: '◐', light: '☀', dark: '☾' }[mode];
    const title = `${text('外观', 'Appearance')}: ${names[mode]}`;
    themeButton.title = title; themeButton.setAttribute('aria-label', title);
  }
  document.querySelectorAll('.theme-menu [data-theme-mode]').forEach(button => button.setAttribute('aria-pressed', String(button.dataset.themeMode === mode)));
};
themeButton?.addEventListener('click', () => {
  const open = themeMenu.hidden;
  closeNav(); themeMenu.hidden = !open; themeButton.setAttribute('aria-expanded', String(open));
});
document.querySelectorAll('.theme-menu [data-theme-mode]').forEach(button => button.addEventListener('click', () => {
  window.KUBE_RESOURCE_FCST_THEME.setMode(button.dataset.themeMode); closeTheme(); themeButton.focus();
}));
document.addEventListener('click', event => { if (!event.target.closest('.theme-control')) closeTheme(); });
window.addEventListener('kube-resource-fcst-themechange', refreshTheme);
refreshTheme();

document.querySelectorAll('[data-copy]').forEach(button => button.addEventListener('click', async () => {
  const code = document.getElementById(button.dataset.copy);
  try { await navigator.clipboard.writeText(code.textContent); button.textContent = text('已复制', 'Copied'); }
  catch {
    const range = document.createRange(); range.selectNodeContents(code);
    const selection = getSelection(); selection.removeAllRanges(); selection.addRange(range);
    button.textContent = text('已选中，请手动复制', 'Selected — copy manually');
  }
  setTimeout(() => { button.textContent = text('复制', 'Copy'); }, 2500);
}));

const tocLinks = [...document.querySelectorAll('.page-toc nav a')];
if (tocLinks.length && 'IntersectionObserver' in window) {
  const observer = new IntersectionObserver(entries => {
    for (const entry of entries) {
      if (!entry.isIntersecting) continue;
      tocLinks.forEach(link => {
        const active = link.hash === `#${entry.target.id}`;
        link.classList.toggle('current', active);
        if (active) link.setAttribute('aria-current', 'location'); else link.removeAttribute('aria-current');
      });
    }
  }, { rootMargin: '-12% 0px -65% 0px' });
  document.querySelectorAll('.article-body h2[id]').forEach(heading => observer.observe(heading));
}

const dialog = document.getElementById('search-dialog');
const input = document.getElementById('doc-search');
const results = document.getElementById('search-results');
const status = document.getElementById('search-status');
let indexPromise;
let searchIndex = [];
const highlighted = (element, value, query) => {
  const offset = value.toLocaleLowerCase().indexOf(query.toLocaleLowerCase());
  if (!query || offset < 0) { element.textContent = value; return; }
  element.append(document.createTextNode(value.slice(0, offset)));
  const mark = document.createElement('mark'); mark.textContent = value.slice(offset, offset + query.length);
  element.append(mark, document.createTextNode(value.slice(offset + query.length)));
};
function renderSearch() {
  const query = input.value.trim().toLocaleLowerCase();
  const terms = query.split(/\s+/).filter(Boolean);
  const found = searchIndex.filter(item => terms.every(term => `${item.title} ${item.summary} ${item.text}`.toLocaleLowerCase().includes(term)))
    .sort((a, b) => Number(b.title.toLocaleLowerCase().includes(query)) - Number(a.title.toLocaleLowerCase().includes(query)));
  results.replaceChildren();
  status.textContent = query ? text(`找到 ${found.length} 篇相关文档`, `${found.length} matching article${found.length === 1 ? '' : 's'}`) : text('选择一篇文档，或输入关键词搜索', 'Choose an article or search by keyword');
  for (const item of found.slice(0, 8)) {
    const li = document.createElement('li'); const link = document.createElement('a');
    link.href = localUrl(item.path).href;
    const heading = document.createElement('strong'); highlighted(heading, item.title, query);
    const description = document.createElement('p');
    const position = query ? item.text.toLocaleLowerCase().indexOf(query) : -1;
    const snippet = position >= 0 && !item.summary.toLocaleLowerCase().includes(query) ? `${position > 25 ? '…' : ''}${item.text.slice(Math.max(0, position - 25), position + 95)}…` : item.summary;
    highlighted(description, snippet, query); link.append(heading, description); li.append(link); results.append(li);
  }
}
async function openSearch() {
  closeNav(); closeTheme(); closeSidebar();
  if (!dialog.open) dialog.showModal();
  input.focus();
  status.textContent = text('正在加载文档…', 'Loading documentation…');
  try {
    if (!indexPromise) indexPromise = fetch(new URL('assets/search-index.json', root)).then(response => {
      if (!response.ok) throw new Error('Search index unavailable'); return response.json();
    }).catch(error => { indexPromise = undefined; throw error; });
    searchIndex = (await indexPromise).filter(item => item.lang === lang);
    renderSearch();
  } catch {
    results.replaceChildren();
    status.textContent = text('暂时无法搜索，请从文档目录继续阅读。', 'Search is unavailable. Browse the documentation instead.');
    const li = document.createElement('li'); const link = document.createElement('a');
    link.href = localUrl(`${english ? 'en/' : ''}docs/index.html`).href;
    link.textContent = text('进入文档中心 →', 'Open documentation →'); li.append(link); results.append(li);
  }
}
document.querySelectorAll('[data-search-open]').forEach(button => button.addEventListener('click', openSearch));
document.querySelector('[data-search-close]')?.addEventListener('click', () => dialog.close());
dialog?.addEventListener('click', event => { if (event.target === dialog) {
  const bounds = dialog.getBoundingClientRect();
  if (event.clientX < bounds.left || event.clientX > bounds.right || event.clientY < bounds.top || event.clientY > bounds.bottom) dialog.close();
} });
input?.addEventListener('input', renderSearch);
dialog?.addEventListener('keydown', event => {
  const links = [...results.querySelectorAll('a')];
  if (!links.length) return;
  const selected = links.indexOf(document.activeElement);
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault();
    const next = event.key === 'ArrowDown' ? (selected + 1) % links.length : (selected < 0 ? links.length - 1 : (selected - 1 + links.length) % links.length);
    links[next].focus();
  } else if (event.key === 'Enter' && document.activeElement === input) {
    event.preventDefault(); links[0].click();
  }
});
document.querySelectorAll('.search-trigger kbd').forEach(kbd => { kbd.textContent = /Mac|iPhone|iPad/.test(navigator.platform) ? '⌘ K' : 'Ctrl K'; });
document.addEventListener('keydown', event => {
  if ((event.ctrlKey || event.metaKey) && event.key.toLocaleLowerCase() === 'k') { event.preventDefault(); openSearch(); }
  if (event.key === 'Escape') {
    if (dialog?.open) { event.preventDefault(); dialog.close(); }
    if (themeButton?.getAttribute('aria-expanded') === 'true') themeButton.focus();
    if (sidebarButton?.getAttribute('aria-expanded') === 'true') sidebarButton.focus();
    closeTheme(); closeNav(); closeSidebar();
  }
});
