(() => {
  const routes = { overview: 'index', quickstart: 'getting-started', architecture: 'concepts', data: 'metrics', algorithms: 'algorithms', evaluation: 'evaluation', execution: 'scaling', safety: 'safety', operations: 'monitoring', status: 'concepts', faq: 'faq' };
  const old = location.hash.slice(1);
  const target = new URL(`docs/${routes[old] || 'index'}.html`, location.href);
  target.search = location.search;
  if (old === 'status') target.hash = 'boundaries';
  location.replace(target.href);
})();
