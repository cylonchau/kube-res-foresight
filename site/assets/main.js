'use strict';
const english = document.documentElement.lang === 'en';
const copyText = english ? 'Copy' : '复制';
const themeToggle = document.getElementById('theme-toggle');
const themeMenu = document.getElementById('theme-menu');
const closeThemeMenu = () => {
  if (themeMenu) themeMenu.hidden = true;
  themeToggle?.setAttribute('aria-expanded', 'false');
};
const refreshThemeControls = () => {
  const mode = window.KUBE_RESOURCE_FCST_THEME?.getMode() || 'auto';
  const names = english ? { auto: 'System', light: 'Light', dark: 'Dark' } : { auto: '跟随系统', light: '浅色', dark: '深色' };
  if (themeToggle) {
    themeToggle.querySelector('span').textContent = { auto: '◐', light: '☀', dark: '☾' }[mode];
    const label = `${english ? 'Appearance' : '外观'}: ${names[mode]}`;
    themeToggle.setAttribute('aria-label', label);
    themeToggle.title = label;
  }
  document.querySelectorAll('.theme-menu [data-theme-mode]').forEach(button => button.setAttribute('aria-pressed', String(button.dataset.themeMode === mode)));
};
themeToggle?.addEventListener('click', () => {
  const open = themeMenu.hidden;
  themeMenu.hidden = !open;
  themeToggle.setAttribute('aria-expanded', String(open));
});
document.querySelectorAll('.theme-menu [data-theme-mode]').forEach(button => button.addEventListener('click', () => {
  window.KUBE_RESOURCE_FCST_THEME?.setMode(button.dataset.themeMode);
  closeThemeMenu();
  themeToggle.focus();
}));
document.addEventListener('click', event => { if (!event.target.closest('.theme-control')) closeThemeMenu(); });
window.addEventListener('kube-resource-fcst-themechange', refreshThemeControls);
refreshThemeControls();
const languageRoot = new URL(document.documentElement.dataset.siteRoot || './', location.href);
document.querySelectorAll('[data-language]').forEach(link => {
  const language = link.dataset.language;
  const filename = location.pathname.endsWith('docs.html') ? 'docs.html' : 'index.html';
  const target = new URL(`${language === 'en' ? 'en/' : ''}${filename}`, languageRoot);
  target.searchParams.set('lang', language);
  target.hash = location.hash;
  link.href = target.href;
  link.addEventListener('click', () => {
    const destination = new URL(link.href);
    destination.hash = location.hash;
    link.href = destination.href;
    try { localStorage.setItem('kube-resource-fcst-language', language); } catch { /* The language is also in the URL. */ }
  });
});
// Keep an explicit choice across pages, including browsers with storage disabled.
document.querySelectorAll('a[href]').forEach(link => {
  const raw = link.getAttribute('href');
  if (raw.startsWith('#') || link.hasAttribute('data-language') || link.hasAttribute('data-repo') || link.hasAttribute('data-source')) return;
  const target = new URL(link.href);
  if (target.origin === location.origin && target.pathname.startsWith(languageRoot.pathname)) {
    target.searchParams.set('lang', english ? 'en' : 'zh');
    link.href = target.href;
  }
});
const site = window.KUBE_RESOURCE_FCST_SITE;
if (site) {
  document.querySelectorAll('[data-repo]').forEach(link => { link.href = site.repository; });
  document.querySelectorAll('[data-source]').forEach(link => {
    link.href = `${site.repository}/blob/${encodeURIComponent(site.branch)}/${link.dataset.source}`;
  });
}
const menu = document.querySelector('.menu-toggle');
menu?.addEventListener('click', () => {
  const open = menu.getAttribute('aria-expanded') !== 'true';
  menu.setAttribute('aria-expanded', String(open));
  document.querySelector('#site-nav').classList.toggle('is-open', open);
});
document.querySelectorAll('#site-nav a').forEach(link => link.addEventListener('click', () => {
  menu?.setAttribute('aria-expanded', 'false');
  document.querySelector('#site-nav').classList.remove('is-open');
}));
document.addEventListener('keydown', event => {
  if (event.key === 'Escape') {
    if (themeToggle?.getAttribute('aria-expanded') === 'true') themeToggle.focus();
    closeThemeMenu();
    if (menu?.getAttribute('aria-expanded') === 'true') menu.focus();
    menu?.setAttribute('aria-expanded', 'false');
    document.querySelector('#site-nav')?.classList.remove('is-open');
  }
});
const stages = english ? {
  collect: ['01 / COLLECT', 'Check your data before forecasting', 'Connect CPU, memory, and request metrics for a workload. Inspect history, gaps, and freshness, then align the dataset for dependable forecast inputs.'],
  predict: ['02 / PREDICT', 'Compare candidates on the same workload', 'Use backtests and arriving actuals to compare errors and peak underestimation. Scores, rankings, and adoption thresholds determine which candidates can inform recommendations.'],
  decide: ['03 / DECIDE', 'Review advice with clear sources and expiry', 'Combine workload constraints, schedules, OOM protection, and fallback into resource and replica advice. Review sources, expiry, and degradation reasons before authorizing actions.']
} : {
  collect: ['01 / COLLECT', '先确认数据可用，再开始预测', '接入工作负载的 CPU、内存、请求量等指标，查看历史趋势、缺样与新鲜度。通过数据集统一统计口径，为预测准备可信的输入。'],
  predict: ['02 / PREDICT', '让候选算法接受同一份历史检验', '比较各算法对同一工作负载的预测结果，通过历史回测与后续实测检查误差和峰值低估。结合评分、排名与采用门槛，判断哪些候选可用于推荐。'],
  decide: ['03 / DECIDE', '输出可解释、有有效期的推荐', '结合 CPU、内存、吞吐等约束与固定周期、OOM 保护和兜底策略，形成资源与副本建议。查看采用来源、有效期和降级原因，授权后再应用并追踪结果。']
};
document.querySelectorAll('[data-stage]').forEach(button => button.addEventListener('click', () => {
  document.querySelectorAll('[data-stage]').forEach(item => { item.classList.toggle('active', item === button); item.setAttribute('aria-pressed', String(item === button)); });
  const stage = stages[button.dataset.stage];
  ['stage-number', 'stage-title', 'stage-description'].forEach((id, index) => { document.getElementById(id).textContent = stage[index]; });
}));
document.querySelectorAll('[data-copy]').forEach(button => button.addEventListener('click', async () => {
  const code = document.getElementById(button.dataset.copy);
  try {
    await navigator.clipboard.writeText(code.textContent);
    button.textContent = english ? 'Copied' : '已复制';
  } catch {
    const range = document.createRange(); range.selectNodeContents(code);
    const selection = window.getSelection(); selection.removeAllRanges(); selection.addRange(range);
    button.textContent = english ? 'Selected — copy manually' : '已选中，请手动复制';
  }
  setTimeout(() => { button.textContent = copyText; }, 2500);
}));
const search = document.getElementById('doc-search');
search?.addEventListener('input', () => {
  const query = search.value.trim().toLocaleLowerCase();
  let matches = 0;
  document.querySelectorAll('.docs-sidebar a[data-topic]').forEach(link => {
    const section = document.querySelector(link.getAttribute('href'));
    const match = !query || `${link.textContent} ${link.dataset.topic} ${section?.textContent || ''}`.toLocaleLowerCase().includes(query);
    link.hidden = !match; if (match) matches++;
  });
  document.getElementById('search-status').textContent = query ? english ? `${matches} related chapter${matches === 1 ? '' : 's'}` : `找到 ${matches} 个相关章节` : '';
});
if ('IntersectionObserver' in window) {
  const observer = new IntersectionObserver(entries => {
    entries.forEach(entry => {
      if (!entry.isIntersecting) return;
      document.querySelectorAll('.docs-sidebar a[data-topic]').forEach(link => {
        const current = link.hash === `#${entry.target.id}`;
        link.classList.toggle('current', current);
        if (current) link.setAttribute('aria-current', 'location'); else link.removeAttribute('aria-current');
      });
    });
  }, { rootMargin: '-15% 0px -65% 0px' });
  document.querySelectorAll('.docs-content > section[id]').forEach(section => observer.observe(section));
}
