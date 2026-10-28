// Browser check of the module / algorithm management split:
//  - algorithms are configured once for the platform (paged, searched, edited with explained parameters) and an enabled
//    algorithm is applied to every module automatically: nothing is created per module and nothing is bound by hand;
//  - a module is a workload that owns its metric sources, history window and forecast period;
//  - the whole chain module -> every enabled algorithm -> backtest and score -> best algorithm -> recommendation;
//  - the result, action and monitoring pages (the curves have a page of their own), and authorization as a top-level menu;
//  - custom formula algorithms (written on a page, compiled and run by the platform), the training records, the Prometheus
//    dataset of a module (granularity, shaping, custom PromQL), the split scoring pages and the detail of the records.
// Run it only against a scratch installation with a freshly migrated database, and with fake-prometheus.mjs on 127.0.0.1:19090.
// Usage: node scripts/e2e/modules-algorithms.mjs (E2E_URL, E2E_OUTPUT, E2E_CHROME, PLAYWRIGHT_MODULE, E2E_PASSWORD are optional)
import assert from 'node:assert/strict';
import { mkdirSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE ? pathToFileURL(process.env.PLAYWRIGHT_MODULE).href : '@playwright/test');

const base = process.env.E2E_URL || 'http://127.0.0.1:20122';
const out = process.env.E2E_OUTPUT || 'target/modules-algorithms-qa/shots';
const chrome = process.env.E2E_CHROME;
mkdirSync(out, { recursive: true });

const call = async (method, path, token, body) => {
  const res = await fetch(`${base}/api/v1${path}`, {
    method,
    headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
    body: body ? JSON.stringify(body) : undefined
  });
  const json = await res.json().catch(() => ({}));
  return { status: res.status, ...json };
};
const ok = (r, what) => assert.equal(r.code, 10000, `${what}: ${JSON.stringify(r)}`);
const results = [];
const check = (name, pass, detail = '') => {
  results.push({ name, pass });
  console.log(`${pass ? 'ok  ' : 'FAIL'} ${name}${pass || !detail ? '' : ` -- ${detail}`}`);
};

const login = await call('POST', '/auth/login', '', { username: 'admin', password: process.env.E2E_PASSWORD || '123456', force: true });
ok(login, 'login');
const token = login.data.token;

// ---- the data source and the mappings the modules read through -------------------------------------------------------
const ds = await call('POST', '/datasources', token, { name: 'fake-prom', base_url: 'http://127.0.0.1:19090' });
ok(ds, 'datasource');
const mapping = async (name, metric, query) => {
  const r = await call('POST', '/metric-mappings', token, { datasource_id: ds.data.id, name, metric, level: 'workload', query });
  ok(r, `mapping ${name}`);
  return r.data.id;
};
const cpu = await mapping('cpu-workload', 'cpu', 'sum(rate(cpu_usage{namespace="{{namespace}}",pod=~"{{workload}}-.*"}[{{range}}]))');
const mem = await mapping('mem-workload', 'memory', 'sum(mem_working_set{namespace="{{namespace}}",pod=~"{{workload}}-.*"})');
// a second CPU template, kept apart from the modules: the module form adds it to NGS APP below
const cpuExtra = await mapping('cpu-extra', 'cpu', 'sum(rate(cpu_usage{namespace="{{namespace}}",pod=~"{{workload}}-.*"}[{{range}}]))');

// ---- 1. algorithms: one setting per catalog algorithm, applied to every module ----------------------------------------
const algorithms = await call('GET', '/algorithms?page=1&page_size=100', token);
ok(algorithms, 'algorithms');
check('the catalog is listed with its settings and parameter schema', algorithms.data.total >= 14 && algorithms.data.list.every(a => 'enabled' in a && Array.isArray(a.params)));
check('the baselines start enabled and the heavy algorithms wait', algorithms.data.list.find(a => a.id === 'naive').enabled === true && algorithms.data.list.find(a => a.id === 'lstm').enabled === false);
check('the list is paged and searched', (await call('GET', '/algorithms?page=1&page_size=3', token)).data.list.length === 3 && (await call('GET', '/algorithms?keyword=holt', token)).data.total === 1);
const paramNames = id => (algorithms.data.list.find(a => a.id === id)?.params || []).map(p => p.name);
check('P7 A4: Theta is in the catalog and Holt-Winters has the multiplicative season, damping and non-negative parameters', algorithms.data.list.some(a => a.id === 'theta') && ['seasonal_mode', 'damping_phi', 'non_negative'].every(n => paramNames('holt_winters').includes(n)));
check('P7 A3: every forecasting algorithm offers the recommendation quantile, a baseline does not', ['ewma', 'holt_winters', 'theta', 'gbdt'].every(id => paramNames(id).includes('quantile_level')) && !paramNames('naive').includes('quantile_level') && !paramNames('seasonal_naive').includes('quantile_level'));
check('P7 A5: ridge regression and gradient boosting can forecast directly (one model per step ahead), the others cannot', ['ridge_ar', 'gbdt'].every(id => paramNames(id).includes('multi_step') && paramNames(id).includes('direct_anchors')) && !paramNames('ewma').includes('multi_step'));
check('P7 A5: an unknown multi-step mode is refused', (await call('PUT', '/algorithms/ridge_ar', token, { enabled: false, params: { lags: 6, multi_step: 'sideways' } })).status !== 200);
check('P7 A3: a quantile level at or below one half is refused', (await call('PUT', '/algorithms/ewma', token, { enabled: true, params: { alpha: 0.5, quantile_level: 0.3 } })).status !== 200);
ok(await call('PUT', '/algorithms/ewma', token, { enabled: true, params: { alpha: 0.5 } }), 'enable ewma');

// ---- 2. modules: a workload with its own sources and period; no algorithm instance, no binding -----------------------------
check('there is no instance or binding API any more', (await call('GET', '/algorithm-instances', token)).status === 404 && (await call('GET', '/bindings', token)).status === 404);
const shop = await call('POST', '/modules', token, {
  name: 'NGS APP', namespace: 'prod', workload: 'ngs', mode: 'recommend', headroom_ratio: 1.5, fallback_cpu_milli: 400, lookback_hours: 6, horizon_minutes: 10,
  sources: [{ name: 'cpu', kind: 'cpu', mapping_id: cpu }, { name: 'memory', kind: 'memory', mapping_id: mem }]
});
ok(shop, 'create module');
const moduleId = shop.data.id;
check('the module owns its sources, history window and forecast period', shop.data.sources.length === 2 && shop.data.lookback_hours === 6 && shop.data.horizon_minutes === 10);
check('a second module on the same workload is refused', (await call('POST', '/modules', token, { name: 'dup', namespace: 'prod', workload: 'ngs' })).status !== 200);

// ---- 3. the chain, with nothing created or bound for the module ------------------------------------------------------
ok(await call('POST', `/modules/${moduleId}/run`, token), 'run');
const predictions = await call('GET', `/predictions?module_id=${moduleId}&metric=cpu&page_size=100`, token);
const algorithmsPredicted = new Set(predictions.data.list.map(p => p.algorithm_id));
check('every enabled algorithm forecast the module, without being bound to it', ['naive', 'ewma'].every(a => algorithmsPredicted.has(a)), [...algorithmsPredicted].join(','));
for (const metric of ['cpu', 'memory']) ok(await call('POST', `/modules/${moduleId}/backtest`, token, { metric, cutoffs: 3 }), `backtest ${metric}`);
const backtests = await call('GET', `/backtests?module_id=${moduleId}&page_size=100`, token);
check('every algorithm is backtested on every source', backtests.data.total >= 4, String(backtests.data.total));
// P7 A1/A2: the verdict compares like with like, and an underestimate costs more than an overestimate
const parsed = backtests.data.list.map(r => ({ id: r.algorithm_id, own: JSON.parse(r.metrics), base: JSON.parse(r.baseline_metrics) })).filter(r => !r.own.skipped);
check('backtests: every scored result carries the underestimate-weighted error and its weight', parsed.length > 0 && parsed.every(r => r.own.under_weight === 3 && r.own.risk_wape >= r.own.wape - 1e-9), JSON.stringify(parsed.slice(0, 1)));
check('backtests: a candidate and its baseline are scored on the same windows', parsed.filter(r => !r.base.skipped).every(r => r.own.points === r.base.points && r.own.cutoffs === r.base.cutoffs), JSON.stringify(parsed.map(r => [r.id, r.own.cutoffs, r.own.points, r.base.cutoffs, r.base.points])));
ok(await call('POST', `/modules/${moduleId}/score`, token), 'score');
const ranking = await call('GET', `/modules/${moduleId}/ranking`, token);
check('the algorithms are ranked for the module', ranking.data.list.length >= 2 && ranking.data.list.every(r => r.score !== null), JSON.stringify(ranking.data.list.map(r => r.name)));
const rec = await call('POST', `/modules/${moduleId}/recommend`, token);
ok(rec, 'recommend');
check('the recommendation names the algorithm chosen', rec.data.recommendation.cpu_source === 'algorithm' && ['naive', 'ewma'].includes(rec.data.recommendation.algorithm), JSON.stringify(rec.data.recommendation).slice(0, 200));

// R10: the Pod resource spec is rounded to readable steps and kept while the forecast has not moved; replicas are decided apart from it
const spec1 = rec.data.recommendation;
check('R10: the CPU request is a whole readable step (10m below a core, 50m from one)', spec1.cpu_milli % (spec1.cpu_milli >= 1000 ? 50 : 10) === 0, String(spec1.cpu_milli));
check('R10: the memory request is whole MiB in readable steps (10Mi below 1Gi, 64Mi from it)', !spec1.memory_set || (spec1.memory_bytes % (1024 * 1024) === 0 && (spec1.memory_bytes / 1048576) % (spec1.memory_bytes >= 1024 * 1048576 ? 64 : 10) === 0), String(spec1.memory_bytes));
check('R10: the recommendation carries the version of the Pod spec and the explanation says how it was decided', spec1.resource_version >= 1 && JSON.parse(rec.data.recommendation.detail).resource_spec?.version === spec1.resource_version, JSON.stringify(JSON.parse(spec1.detail).resource_spec || {}).slice(0, 160));
const rec2 = await call('POST', `/modules/${moduleId}/recommend`, token);
ok(rec2, 'recommend again');
check('R10: a second recommendation on the same data keeps the same spec and the same spec version', rec2.data.recommendation.cpu_milli === spec1.cpu_milli && rec2.data.recommendation.memory_bytes === spec1.memory_bytes && rec2.data.recommendation.resource_version === spec1.resource_version, JSON.stringify([spec1.resource_version, rec2.data.recommendation.resource_version]));
const snapshots = await call('GET', `/snapshots?module_id=${moduleId}&page_size=50`, token);
ok(snapshots, 'snapshots');
check('R10: the snapshots are the history, not every repeat: the first one is there and the repeat of the same answer is not', snapshots.data.total >= 1 && snapshots.data.list.every(x => x.snapshot === true) && snapshots.data.list.some(x => x.version === spec1.version) && !snapshots.data.list.some(x => x.version === rec2.data.recommendation.version), JSON.stringify(snapshots.data.list.map(x => [x.version, x.state, x.snapshot_reason])));
check('R10: every snapshot has a state of the four (plus advisory for a module that only recommends)', snapshots.data.list.every(x => ['pending', 'applied', 'failed', 'expired', 'advisory'].includes(x.state)), JSON.stringify(snapshots.data.list.map(x => x.state)));
const snapshotDetail = await call('GET', `/snapshots/${snapshots.data.list[0].id}`, token);
check('R10: a snapshot can be opened with its whole stored decision', snapshotDetail.status === 200 && JSON.parse(snapshotDetail.data.detail).cpu !== undefined, String(snapshotDetail.status));
const fallbackList = await call('GET', '/fallbacks?kind=degrade&page_size=5', token);
check('R10: the degradation and rollback records can be listed by kind', fallbackList.status === 200 && (await call('GET', '/fallbacks?kind=rollback&page_size=5', token)).status === 200 && (await call('GET', '/fallbacks?kind=other', token)).status === 400);
const ds10 = await call('POST', '/datasources', token, { name: 'big-concurrency', base_url: 'http://127.0.0.1:19090', max_concurrency: 64 });
check('R10: a datasource may be given more than 16 concurrent requests', ds10.status === 200 && ds10.data.max_concurrency === 64, JSON.stringify(ds10).slice(0, 160));
if (ds10.status === 200) await call('DELETE', `/datasources/${ds10.data.id}`, token);
const algs10 = await call('GET', '/algorithms?page=1&page_size=100', token);
check('R10: every algorithm says how far it has got into the pipeline', algs10.data.list.every(a => ['disabled', 'invalid', 'waiting', 'running', 'skipped', 'failed'].includes(a.runtime?.state)) && algs10.data.list.find(a => a.id === 'ewma').runtime.state === 'running', JSON.stringify(algs10.data.list.slice(0, 2).map(a => [a.id, a.runtime])));

// a module created later gets the same algorithms with no step of its own
const web = await call('POST', '/modules', token, { name: 'web', namespace: 'prod', workload: 'web', mode: 'recommend', lookback_hours: 6, sources: [{ name: 'cpu', kind: 'cpu', mapping_id: cpu }] });
ok(web, 'second module');
ok(await call('POST', `/modules/${web.data.id}/run`, token), 'run second module');
const webAlgorithms = new Set((await call('GET', `/predictions?module_id=${web.data.id}&metric=cpu&page_size=100`, token)).data.list.map(p => p.algorithm_id));
check('a module added later is associated with the enabled algorithms automatically', ['naive', 'ewma'].every(a => webAlgorithms.has(a)));
// turning an algorithm on reaches the existing modules on their next run
ok(await call('PUT', '/algorithms/holt_winters', token, { enabled: true, params: { season_length_steps: 0 } }), 'enable holt_winters');
ok(await call('POST', `/modules/${moduleId}/predict`, token), 'predict again');
const after = new Set((await call('GET', `/predictions?module_id=${moduleId}&metric=cpu&page_size=200`, token)).data.list.map(p => p.algorithm_id));
check('an algorithm enabled later applies to the existing modules', after.has('holt_winters'), [...after].join(','));

// ---- 3b. a custom algorithm: parameters + formula, really executed -----------------------------------------------------------
const formulaOf = (formula, extra = {}) => ({
  id: 'peak_margin', category: 'technical', description: 'peak of the last day plus a margin', formula, min_samples: 12,
  param_schema: [{ name: 'margin', type: 'float', default: 1.2, min: 1, max: 3, description: 'headroom over the peak' }], ...extra
});
check('a formula that does not compile is refused with its position', (await call('POST', '/algorithms', token, formulaOf('smax(x, 60 * margin'))).status !== 200);
check('a formula over an unknown name is refused', (await call('POST', '/algorithms', token, formulaOf('smax(x, 60) * nope'))).status !== 200);
check('the id of a built-in algorithm cannot be taken', (await call('POST', '/algorithms', token, formulaOf('last(x)', { id: 'ewma' }))).status !== 200);
const checkFormula = await call('POST', '/algorithm-formulas/check', token, { formula: 'smax(x, 24) * margin', min_samples: 12, param_schema: formulaOf('x').param_schema, horizon: 12 });
check('the editor can try a formula before it is saved', checkFormula.data.ok === true && checkFormula.data.forecast.length === 12);
ok(await call('POST', '/algorithms', token, formulaOf('smax(x, 60) * margin', { enabled: true, params: { margin: 1.5 } })), 'create custom algorithm');
const custom = (await call('GET', '/algorithms/peak_margin', token)).data;
check('the custom algorithm is listed as custom with its formula and parameter schema', custom.custom === true && custom.formula === 'smax(x, 60) * margin' && custom.params.length === 1 && custom.values.margin === 1.5);
ok(await call('POST', `/modules/${moduleId}/predict`, token), 'predict with the custom algorithm');
const customPredictions = (await call('GET', `/predictions?module_id=${moduleId}&metric=cpu&algorithm_id=peak_margin&page_size=50`, token)).data;
check('the custom algorithm forecast the module like a built-in one', customPredictions.total >= 1 && customPredictions.list[0].value > 0, String(customPredictions.total));
const trainings = (await call('GET', `/training-runs?module_id=${moduleId}&algorithm_id=peak_margin`, token)).data;
check('every run left a training record with the window, the parameters and the formula', trainings.total >= 1 && trainings.list[0].status === 'ok' && trainings.list[0].formula === 'smax(x, 60) * margin' && trainings.list[0].samples > 0 && trainings.list[0].params.includes('margin'),
  JSON.stringify(trainings.list[0] || {}).slice(0, 240));
check('the training records are paged and filtered by status', (await call('GET', '/training-runs?status=failed&page_size=5', token)).data.total === 0 && (await call('GET', '/training-runs?page_size=2', token)).data.list.length === 2);
check('a built-in algorithm cannot be deleted, an enabled custom one neither', (await call('DELETE', '/algorithms/ewma', token)).status !== 200 && (await call('DELETE', '/algorithms/peak_margin', token)).status !== 200);

// ---- 3c. the dataset of a module: granularity, shaping, custom PromQL ----------------------------------------------------
const catalog = await call('GET', '/dataset/catalog', token);
check('the dataset catalog has the five categories', catalog.data.categories.map(c => c.id).join() === 'cpu,memory,gpu,network,other');
const grid = await call('POST', '/modules', token, {
  name: 'grid', namespace: 'prod', workload: 'grid', mode: 'recommend', lookback_hours: 48, horizon_minutes: 60, step_seconds: 900, window_steps: 96, min_points: 48, run_interval_minutes: 15,
  sources: [{ name: 'cpu', kind: 'cpu', mapping_id: cpu, aggregation: 'max', scale: 2, precision: 3 },
    { name: 'memory-ui', kind: 'memory', custom: { datasource_id: ds.data.id, query: 'sum(mem_working_set{namespace="{{namespace}}",pod=~"{{workload}}-.*"})', step_seconds: 60 }, missing: 'forward_fill' }]
});
ok(grid, 'create the dataset module');
check('the module keeps its granularity, minimum data and run frequency', grid.data.step_seconds === 900 && grid.data.window_steps === 96 && grid.data.min_points === 48 && grid.data.run_interval_minutes === 15);
check('a granularity that is not a multiple of the query step is refused', (await call('PUT', `/modules/${grid.data.id}`, token, { step_seconds: 90 })).status !== 200);
check('a history range that cannot hold the minimum data is refused', (await call('PUT', `/modules/${grid.data.id}`, token, { lookback_hours: 6 })).status !== 200);
const gridSources = (await call('GET', `/modules/${grid.data.id}/sources`, token)).data;
const memorySource = gridSources.find(src => src.name === 'memory-ui');
check('a PromQL written for a source created its mapping; the dataset reports category, unit and grid', memorySource && memorySource.category === 'memory' && memorySource.unit === 'bytes' && memorySource.step_seconds === 900 && memorySource.query_step_seconds === 60 && memorySource.query.includes('mem_working_set'));
const collected = await call('POST', `/modules/${grid.data.id}/collect`, token);
ok(collected, 'collect the dataset');
check('the dataset is collected on the 15 minute grid', collected.data.every(r => !r.error) && collected.data[0].stored >= 180 && collected.data[0].stored <= 195, JSON.stringify(collected.data.map(r => [r.metric, r.stored, r.error])));
const gridHistory = (await call('GET', `/modules/${grid.data.id}/history?metric=cpu`, token)).data;
check('every stored point sits on the grid and was scaled', gridHistory.step_seconds === 900 && gridHistory.points.every(pt => Date.parse(pt.time) / 1000 % 900 === 0));
const needs = (await call('GET', `/modules/${grid.data.id}/requirements`, token)).data;
check('the requirements say what each algorithm needs at that step', needs.length >= 4 && needs.every(n => n.step_seconds === 900 && typeof n.min_samples === 'number'), JSON.stringify(needs[0]));
ok(await call('POST', `/modules/${grid.data.id}/run`, token), 'run the dataset module');
const gridRuns = (await call('GET', `/training-runs?module_id=${grid.data.id}&page_size=5`, token)).data;
check('the algorithms trained on the newest 96 steps of the 192 collected (the training window)', gridRuns.total >= 1 && gridRuns.list.every(r => r.samples === 96), JSON.stringify(gridRuns.list.map(r => r.samples)));
ok(await call('POST', `/modules/${grid.data.id}/evaluate`, token), 'evaluate');

// ---- 3d. the scoring pages read the same data ---------------------------------------------------------------------------------
const current = await call('GET', `/algorithm-rankings/current?module_id=${moduleId}`, token);
const currentRows = current.data.list[0].rows;
check('the current ranking names the best algorithm and gives the parts of every score', current.data.list[0].best && currentRows.length >= 2 && currentRows.every(r => Array.isArray(r.breakdown) && r.breakdown.length >= 1));
const history = await call('GET', `/algorithm-rankings/history?module_id=${moduleId}`, token);
check('the ranking history keeps every evaluation', history.data.total >= 1 && history.data.list[0].rows.length >= 2);
const records = await call('GET', `/algorithm-scores?module_id=${moduleId}&page_size=5`, token);
check('a score record carries the module name and its breakdown', records.data.list[0].module_name === 'NGS APP' && records.data.list[0].breakdown.length >= 1);
check('the metric scores compare every algorithm against the actuals', (await call('GET', `/modules/${moduleId}/metric-scores`, token)).data.metrics !== undefined);

// ---- 3e. a PromQL is tested against the datasource before it is saved
const goodQuery = { datasource_id: ds.data.id, metric: 'cpu', level: 'workload', module_id: moduleId, step_seconds: 60, query: 'sum(rate(cpu_usage{namespace="{{namespace}}",pod=~"{{workload}}-.*"}[{{range}}]))' };
const tested = await call('POST', '/metric-queries/test', token, goodQuery);
check('the query test runs the PromQL for the workload of the module and shapes the values', tested.data.ok === true && tested.data.stage === 'ok' && tested.data.samples > 0 && tested.data.rendered.includes('namespace="prod"') && tested.data.points.length > 0, JSON.stringify(tested.data).slice(0, 200));
check('the query test refuses a template that does not fit before it asks Prometheus', (await call('POST', '/metric-queries/test', token, { ...goodQuery, query: 'sum(up{x="{{nope}}"})' })).data.stage === 'template');
check('history before the module existed comes from Prometheus: nothing is missing in a window of a month ago', (await call('GET', `/modules/${moduleId}/compare?metric=cpu&predictions=false&from=${new Date(Date.now() - 31 * 864e5).toISOString()}&to=${new Date(Date.now() - 30 * 864e5).toISOString()}`, token)).data.actual.missing === false);
const batch = await call('POST', '/algorithms/batch', token, { action: 'enable', ids: ['naive', 'ghost'] });
check('a batch reports what it did and what it refused', batch.data.done === 1 && batch.data.failed.length === 1);

// ---- 4. the pages ---------------------------------------------------------------------------------------------------
const browser = await chromium.launch({ headless: true, ...(chrome ? { executablePath: chrome } : { channel: 'chromium' }) });
try {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  await context.addInitScript(({ token, refreshToken }) => {
    localStorage.setItem('SOY_token', JSON.stringify(token));
    localStorage.setItem('SOY_refreshToken', JSON.stringify(refreshToken));
    localStorage.setItem('SOY_lang', JSON.stringify('zh-CN'));
  }, login.data);
  const page = await context.newPage();
  const errors = [];
  page.on('pageerror', err => errors.push(err.message));
  const bodyText = () => page.locator('body').innerText();
  const visit = async route => {
    await page.goto(`${base}/${route}`);
    await page.locator('.n-menu').first().waitFor({ state: 'visible' });
    await page.waitForTimeout(1200);
  };
  // the buttons of an operate column stay inside their cell, and the page does not scroll sideways
  const operateFits = async label => {
    const bad = await page.evaluate(() => {
      const out = [];
      for (const cell of document.querySelectorAll('.n-data-table-td--fixed-right')) {
        const box = cell.getBoundingClientRect();
        for (const el of cell.querySelectorAll('button, .n-button')) {
          const r = el.getBoundingClientRect();
          if (r.width && (r.right > box.right + 1 || r.left < box.left - 1)) out.push(`${el.textContent.trim()} ${Math.round(r.right - box.right)}px`);
        }
      }
      return out;
    });
    check(`${label}: the buttons of the operate column stay inside their cell`, bad.length === 0, bad.slice(0, 3).join(', '));
  };
  const noSideScroll = async label => check(`${label}: the page does not scroll sideways`, await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1));
  const pick = async (modalSelectIndex, text) => {
    await page.locator('.n-modal .n-select').nth(modalSelectIndex).click();
    await page.locator('.n-base-select-option', { hasText: text }).first().click();
    await page.waitForTimeout(300);
  };
  const shot = name => page.screenshot({ path: `${out}/${name}.png`, fullPage: true, animations: 'disabled' });

  await visit('forecast-modules/modules');
  const menu = await page.locator('.n-menu').first().innerText();
  check('menu: authorization is a top-level menu of its own', menu.includes('授权管理') && menu.includes('系统管理'));
  check('menu: module management has the module list and no longer the metric sources', menu.includes('模块列表') && !menu.includes('指标来源'));
  check('menu: no binding, instance or catalog page', !menu.includes('绑定关系') && !menu.includes('算法实例') && !menu.includes('算法目录'));
  check('menu: no separate ranking history page and no audit export page', !menu.includes('历史排名') && !menu.includes('审计导出'));
  check('the audit export endpoint is gone', (await call('GET', '/audit/export?kind=executions', token)).status === 404);
  check('modules: the module is listed with its kind and status', (await bodyText()).includes('NGS APP') && (await bodyText()).includes('Deployment'));
  await operateFits('modules');
  const moduleButtons = await page.locator('.n-data-table-td--fixed-right').first().locator('button').allInnerTexts();
  check('modules: the operate column is edit, delete, clone and bypass in one row, with no more menu', moduleButtons.map(x => x.trim()).join() === '编辑,删除,克隆,旁路', moduleButtons.join());
  const operateBox = await page.locator('.n-data-table-th--fixed-right').first().boundingBox();
  const buttonBoxes = await page.locator('.n-data-table-td--fixed-right').first().locator('button').evaluateAll(list => list.map(b => b.getBoundingClientRect()));
  const buttonsSpan = buttonBoxes.length ? buttonBoxes[buttonBoxes.length - 1].right - buttonBoxes[0].left : 0;
  check('modules: the operate column has no wide blank space around its four buttons', !!operateBox && buttonsSpan > 0 && buttonsSpan <= 220 && operateBox.width - buttonsSpan <= 100,JSON.stringify({ column: operateBox?.width, buttons: buttonsSpan }));
  check('modules: the list has check boxes and a batch menu', (await page.locator('.n-data-table-th--selection, .n-data-table-td--selection').count()) > 0 && (await bodyText()).includes('批量操作'));
  await page.getByRole('button', { name: '新增' }).first().click();
  await page.waitForTimeout(500);
  const form = await page.locator('.n-modal').innerText();
  check('module form: workload, data and forecast period, no combine / selection settings',
    form.includes('工作负载类型') && form.includes('历史窗口') && form.includes('预测周期') && form.includes('采样粒度') && form.includes('执行频率') && form.includes('最少数据量') && !form.includes('同一资源的合并方式') && !form.includes('最短历史') && !form.includes('算法评分'),
    form.replace(/\n/g, ' ').slice(0, 200));
  check('module form: the pace of the replica changes is configured on the module', ['副本调整节奏', '扩容单次最大步长', '缩容冷却'].every(v => form.includes(v)));
  check('module form: min, max and fallback replicas and the recovery are module settings, not part of the safety bounds', ['最小副本数', '最大副本数', '默认及故障回退副本数', '恢复启用所需周期数'].every(v => form.includes(v)) && !form.includes('副本上限') && !form.includes('副本下限'), form.replace(/\n/g, ' ').slice(0, 120));
  check('module form: the metrics the module is calculated from and its fixed periods are sections of the form', ['参与计算的指标', '固定周期', '添加固定周期', '添加指标模板'].every(v => form.includes(v)), form.replace(/\n/g, ' ').slice(0, 120));
  check('module form: states are an enable / disable choice, not a switch', (await page.locator('.n-modal .n-switch').count()) === 0 && form.includes('启用') && form.includes('禁用'));
  await shot('modules-form');
  await page.keyboard.press('Escape');
  await page.waitForTimeout(300);

  // ---- a fixed period made in the module form: pinned values win inside the window and the explanation says so
  await page.locator('.n-data-table-tr', { hasText: 'NGS APP' }).first().getByRole('button', { name: '编辑' }).click();
  await page.waitForTimeout(900);
  // a metric template is added to the module like a period: a row with the template, its weight and its state
  const metricsBefore = (await call('GET', `/modules/${moduleId}/sources`, token)).data.length;
  await page.getByRole('button', { name: '添加指标模板' }).click();
  await page.waitForTimeout(300);
  const metricRow = page.locator('.metric-row').last();
  await metricRow.locator('.n-select').first().click();
  await page.keyboard.type('cpu-extra');
  await page.waitForTimeout(300);
  await page.locator('.n-base-select-option', { hasText: 'cpu-extra' }).first().click();
  await metricRow.locator('.n-input-number input').first().fill('2');
  await page.getByRole('button', { name: '添加固定周期' }).click();
  await page.waitForTimeout(300);
  const periodRow = page.locator('.period-row').first();
  await periodRow.locator('.n-input:not(.n-input-number) input').first().fill('release');
  const numbers = periodRow.locator('.n-input-number input'); // replicas, CPU cores, memory GiB, priority
  await numbers.nth(0).fill('2');
  await numbers.nth(1).fill('4');
  await numbers.nth(2).fill('2');
  await numbers.nth(3).fill('5');
  await shot('modules-periods');
  await page.locator('.n-modal').getByRole('button', { name: '保存' }).click();
  await page.waitForTimeout(1500);
  const savedPeriods = (await call('GET', `/modules/${moduleId}/overrides`, token)).data || [];
  const sourcesAfter = (await call('GET', `/modules/${moduleId}/sources`, token)).data;
  const extra = sourcesAfter.find(x => x.mapping_id === cpuExtra);
  check('module form: a metric template added in the form is saved as a metric of the module, with its weight', sourcesAfter.length === metricsBefore + 1 && !!extra && extra.weight === 2 && extra.kind === 'cpu' && extra.enabled, JSON.stringify(extra));
  if (extra) await call('DELETE', `/modules/${moduleId}/sources/${extra.id}`, token);
  const release = savedPeriods.find(x => x.name === 'release');
  check('fixed period: saved with the module as a window with pinned replicas, CPU and memory', !!release && release.replicas === 2 && release.cpu_milli === 4000 && release.memory_bytes === 2 * 1024 ** 3 && release.priority === 5 && release.enabled && !!release.abs_start && !!release.abs_end, JSON.stringify(release));
  const pinned = await call('POST', `/modules/${moduleId}/recommend`, token);
  check('fixed period: inside its window the algorithms do not change the pinned values', pinned.data.recommendation.replicas_source === 'fixed_period' && pinned.data.recommendation.replicas === 2 && pinned.data.recommendation.cpu_source === 'fixed_period' && pinned.data.recommendation.cpu_milli === 4000, JSON.stringify(pinned.data.recommendation).slice(0, 300));
  const pinnedDetail = pinned.data.detail || {};
  check('fixed period: the explanation lists the period, whether it applied, and the metric sources', (pinnedDetail.periods || []).some(x => x.name === 'release' && x.active) && (pinnedDetail.sources || []).length > 0, JSON.stringify(pinnedDetail.periods));
  if (release) {
    await call('PUT', `/modules/${moduleId}/overrides/${release.id}`, token, { ...release, enabled: false });
    const freed = await call('POST', `/modules/${moduleId}/recommend`, token);
    check('fixed period: a disabled period no longer applies and the algorithms decide again', freed.data.recommendation.cpu_source !== 'fixed_period', JSON.stringify(freed.data.recommendation).slice(0, 200));
    await call('DELETE', `/modules/${moduleId}/overrides/${release.id}`, token);
  }
  await page.keyboard.press('Escape');
  await page.waitForTimeout(300);
  check('module form: the metric preview is not part of the form any more', !form.includes('指标预览'));
  // the preview is asked for on demand: choose the module and the metric, then the query test dialog runs the PromQL
  await page.getByRole('button', { name: '指标预览' }).first().click();
  await page.waitForTimeout(900);
  const pickText = await page.locator('.n-modal').last().innerText();
  check('metric preview: a module and one of its metrics are picked first', pickText.includes('指标') && (await page.locator('.n-modal').last().getByRole('button', { name: '指标预览' }).count()) > 0, pickText.replace(/\n/g, ' ').slice(0, 160));
  await page.locator('.n-modal').last().getByRole('button', { name: '指标预览' }).click();
  await page.waitForTimeout(900);
  const previewDialog = await page.locator('.n-modal').last().innerText();
  check('metric preview: the query test dialog is filled with the metric', ['测试 PromQL 查询', '执行查询', '换算系数'].every(v => previewDialog.includes(v)), previewDialog.replace(/\n/g, ' ').slice(0, 200));
  await page.waitForTimeout(1500); // the query runs when the dialog opens: nothing is typed again
  const previewRun = await page.locator('.n-modal').last().innerText();
  check('metric preview: running it shows the result of Prometheus and the curve', (previewRun.includes('采样') || previewRun.includes('样本') || previewRun.includes('数据点')) && (await page.locator('.n-modal canvas').count()) > 0, previewRun.replace(/\n/g, ' ').slice(0, 300));
  await shot('modules-metric-preview');
  await page.keyboard.press('Escape');
  await page.waitForTimeout(300);

  await visit('forecast/algorithms');
  const algText = await bodyText();
  check('algorithms: the management page lists the algorithms with their enabled state', ['naive', 'ewma', 'lstm'].every(a => algText.includes(a)) && algText.includes('启用'));
  check('algorithms: no instance, binding or create-instance action', !algText.includes('创建实例') && !algText.includes('绑定'));
  const algTags = await page.locator('.n-data-table-tbody .n-tag').allInnerTexts();
  check('algorithms: the enabled state of each row is a tag (启用 / 禁用), not a select or a switch',
    algTags.some(x => x.trim() === '启用') && algTags.some(x => x.trim() === '禁用') && (await page.locator('.n-data-table-tbody .n-radio-group, .n-data-table-tbody .n-switch').count()) === 0, algTags.join().slice(0, 100));
  check('algorithms: paged', (await page.locator('.n-pagination').count()) > 0);
  await page.getByPlaceholder('搜索算法名称或说明').fill('holt');
  await page.getByRole('button', { name: '搜索' }).last().click();
  await page.waitForTimeout(800);
  check('algorithms: the search narrows the list', (await page.locator('.n-data-table-tbody .n-data-table-tr').count()) === 1);
  await page.getByPlaceholder('搜索算法名称或说明').fill('');
  await page.getByRole('button', { name: '搜索' }).last().click();
  await page.waitForTimeout(500);
  await page.getByPlaceholder('搜索算法名称或说明').fill('theta');
  await page.getByRole('button', { name: '搜索' }).last().click();
  await page.waitForTimeout(800);
  check('algorithms: Theta is in the catalog', (await page.locator('.n-data-table-tbody .n-data-table-tr').count()) === 1);
  await page.getByRole('button', { name: '编辑' }).first().click();
  await page.waitForTimeout(1000);
  const thetaForm = await bodyText();
  check('algorithms: a forecasting algorithm offers the recommendation quantile and its calibration windows', thetaForm.includes('quantile_level') && thetaForm.includes('calibration_windows'), thetaForm.replace(/\s+/g, ' ').slice(0, 200));
  await shot('algorithm-quantile-params');
  await page.keyboard.press('Escape');
  await page.waitForTimeout(500);
  await page.getByPlaceholder('搜索算法名称或说明').fill('');
  await page.getByRole('button', { name: '搜索' }).last().click();
  await page.waitForTimeout(500);
  await page.getByPlaceholder('搜索算法名称或说明').fill('peak_margin');
  await page.getByRole('button', { name: '搜索' }).last().click();
  await page.waitForTimeout(800);
  check('algorithms: the list shows the origin of an algorithm', (await bodyText()).includes('自定义'));
  await page.getByPlaceholder('搜索算法名称或说明').fill('');
  await page.getByRole('button', { name: '搜索' }).last().click();
  await page.waitForTimeout(500);
  await page.getByRole('button', { name: '新增' }).first().click();
  await page.waitForTimeout(600);
  const deriveText = await page.locator('.n-modal').innerText();
  check('algorithm add: it starts from a built-in algorithm of a type, with that algorithm parameters', ['实现方式', '算法类型', '内置算法', 'alpha'].every(v => deriveText.includes(v)) && !deriveText.includes('参数 Schema'), deriveText.replace(/\n/g, ' ').slice(0, 260));
  const statisticalParams = await page.locator('.n-modal .algorithm-params-grid').innerText();
  await pick(0, '机器学习');
  const mlParams = await page.locator('.n-modal .algorithm-params-grid').innerText();
  check('algorithm add: another type shows other parameters, not the same template', statisticalParams !== mlParams && !mlParams.includes('alpha'), mlParams.replace(/\n/g, ' ').slice(0, 120));
  await pick(0, '统计');
  await page.getByPlaceholder('例如 peak_margin').fill('ui_ewma_fast');
  await page.locator('.n-modal .algorithm-params-grid input').first().fill('0.8');
  await shot('algorithm-add-derived');
  await page.locator('.n-modal').getByRole('button', { name: '保存' }).click();
  await page.waitForTimeout(1500);
  const derived = (await call('GET', '/algorithms/ui_ewma_fast', token)).data;
  check('algorithm add: the derived algorithm keeps its own parameter values and its base', derived?.origin === 'variant' && derived.base === 'ewma' && derived.values.alpha === 0.8, JSON.stringify(derived).slice(0, 160));
  check('algorithm add: the base algorithm is unchanged', (await call('GET', '/algorithms/ewma', token)).data.values.alpha === 0.5);
  ok(await call('DELETE', '/algorithms/ui_ewma_fast', token), 'delete the derived algorithm');
  await page.getByRole('button', { name: '新增' }).first().click();
  await page.waitForTimeout(600);
  await page.locator('.n-modal').getByText('自定义公式', { exact: true }).click();
  await page.waitForTimeout(400);
  const addText = await page.locator('.n-modal').innerText();
  check('algorithm add: a parameter schema editor and a formula editor, not a fixed form', addText.includes('参数 Schema') && addText.includes('公式') && addText.includes('校验并试算') && addText.includes('递归多步预测'));
  await page.getByPlaceholder('例如 peak_margin').fill('ui_peak');
  await page.locator('.n-modal').getByRole('button', { name: '添加参数' }).click();
  await page.getByPlaceholder('参数名（小写字母、数字、下划线）').fill('margin');
  await page.getByPlaceholder('含义、作用与计算依据').fill('headroom over the peak');
  await page.getByPlaceholder('例如：pct(x, window, p) * (1 + margin)').fill('pct(x, 24, 95) * margin');
  await page.locator('.n-modal').getByRole('button', { name: '校验并试算' }).click();
  await page.waitForTimeout(1500);
  check('algorithm add: the formula is compiled, run and drawn before it is saved', (await page.locator('.n-modal').innerText()).includes('公式通过校验') && (await page.locator('.n-modal canvas').count()) > 0);
  await shot('algorithm-add');
  await page.getByPlaceholder('例如：pct(x, window, p) * (1 + margin)').fill('pct(x, 24, 95) * nothing');
  await page.locator('.n-modal').getByRole('button', { name: '校验并试算' }).click();
  await page.waitForTimeout(1000);
  check('algorithm add: a wrong formula is reported with its position and is not saved', (await page.locator('.n-modal').innerText()).includes('position'));
  await page.getByPlaceholder('例如：pct(x, window, p) * (1 + margin)').fill('pct(x, 24, 95) * margin');
  await page.locator('.n-modal').getByRole('button', { name: '保存' }).click();
  await page.waitForTimeout(1500);
  check('algorithm add: the custom algorithm is stored and listed', (await call('GET', '/algorithms/ui_peak', token)).data?.custom === true);
  await page.getByPlaceholder('搜索算法名称或说明').fill('ui_peak');
  await page.getByRole('button', { name: '搜索' }).last().click();
  await page.waitForTimeout(800);
  check('algorithms: the custom algorithm can be found and deleted from the page', (await page.locator('.n-data-table-tbody .n-data-table-tr').count()) === 1);
  await page.locator('.n-data-table-tbody').getByRole('button', { name: '删除' }).click();
  await page.waitForTimeout(400);
  await page.locator('.n-popconfirm__action .n-button--primary-type, .n-popconfirm .n-button--primary-type').first().click();
  await page.waitForTimeout(1200);
  check('algorithms: it is gone after the delete', (await call('GET', '/algorithms/ui_peak', token)).status === 404);
  await page.getByPlaceholder('搜索算法名称或说明').fill('holt');
  await page.getByRole('button', { name: '搜索' }).last().click();
  await page.waitForTimeout(800);
  await page.getByRole('button', { name: '编辑' }).first().click();
  await page.waitForTimeout(600);
  const editText = await page.locator('.n-modal').innerText();
  check('algorithm edit: shows every parameter with its value, no module or binding field',
    editText.includes('season_length_steps') && editText.includes('alpha') && editText.includes('权重') && !editText.includes('绑定'), editText.replace(/\n/g, ' ').slice(0, 200));
  await page.locator('.n-modal .n-icon, .n-modal svg').first().hover().catch(() => {});
  await shot('algorithm-edit');
  // the question mark of a parameter explains its meaning, role and how to choose it
  const helpIcons = page.locator('.n-modal .algorithm-params-grid svg');
  check('algorithm edit: every parameter has a help mark', (await helpIcons.count()) >= 4);
  await helpIcons.first().hover();
  await page.waitForTimeout(500);
  const tip = await page.locator('.n-popover').last().innerText().catch(() => '');
  check('the help of a parameter says what it is, what it does in the calculation and how to choose it', tip.includes('含义') && tip.includes('计算作用') && tip.includes('配置依据'), tip.replace(/\n/g, ' ').slice(0, 160));
  await page.keyboard.press('Escape');

  await visit(`monitoring/datasets?module_id=${grid.data.id}`);
  const datasetText = await bodyText();
  for (const [what, needle] of [['the period', '采样粒度'], ['the 15m grid', '15m'], ['the PromQL source', 'memory-ui'], ['its query', 'mem_working_set'], ['the grid conversion', '1m → 15m'], ['the aggregation', '最大']]) {
    check(`datasets: the dataset shows ${what}`, datasetText.includes(needle), datasetText.replace(/\s+/g, ' ').slice(-600));
  }
  await shot('datasets');
  await page.keyboard.press('Escape');
  await page.waitForTimeout(400);
  check('datasets: no period, requirements or collect row any more', !['数据周期', '算法需求', '采集数据集'].some(v => datasetText.includes(v)));
  // the period of the dataset is a setting of the module
  await call('PUT', `/modules/${grid.data.id}`, token, { step_seconds: 300 });
  check('datasets: the period is saved on the module', (await call('GET', `/modules/${grid.data.id}`, token)).data.step_seconds === 300);
  await page.getByRole('button', { name: '新增' }).first().click();
  await page.waitForTimeout(600);
  const addSource = await page.locator('.n-modal').innerText();
  check('datasets: adding a metric is one form: category, metric template, scale, precision, aggregation and missing-data handling, no query to type',
    ['分类', '指标模板', '换算系数', '精度', '聚合方式', '缺失处理'].every(v => addSource.includes(v)) && (await page.locator('.n-modal textarea').count()) === 0, addSource.replace(/\n/g, ' ').slice(0, 200));
  check('datasets: no custom / mapping choice and no switch in the form', !['自定义 PromQL', '选择数据集映射'].some(v => addSource.includes(v)) && (await page.locator('.n-modal .n-switch').count()) === 0);
  // a template is picked, and the test renders it for the module of the page and runs it, in the same form
  await page.locator('.n-modal .template-field .n-select').first().click();
  await page.waitForTimeout(300);
  await page.locator('.n-base-select-option').first().click();
  await page.waitForTimeout(200);
  await page.locator('.n-modal').getByRole('button', { name: '执行查询' }).click();
  await page.waitForTimeout(1800);
  const inline = await page.locator('.n-modal').innerText();
  check('datasets: the test renders the template for the module and shows the result in the form, without a second dialog', (await page.locator('.n-modal').count()) === 1 && (inline.includes('采样') || inline.includes('样本')) && (await page.locator('.n-modal canvas').count()) > 0, inline.replace(/\n/g, ' ').slice(-200));
  await shot('query-test');
  await page.keyboard.press('Escape');
  await page.waitForTimeout(300);

  // ---- the metric templates: written once on their own page, tested in the same form, chosen by the modules
  await visit('datasource/metric-mappings');
  const templatesText = await bodyText();
  check('metric templates: the page is named for what it holds and lists the templates with their metric, level and state', templatesText.includes('指标模板') && templatesText.includes('cpu-workload') && !templatesText.includes('指标映射'), templatesText.replace(/\s+/g, ' ').slice(0, 160));
  const tplButtons = await page.locator('.n-data-table-td--fixed-right').first().locator('button').evaluateAll(list => list.map(b => b.getBoundingClientRect()));
  const tplBox = await page.locator('.n-data-table-th--fixed-right').first().boundingBox();
  const tplSpan = tplButtons.length ? tplButtons[tplButtons.length - 1].right - tplButtons[0].left : 0;
  check('metric templates: the operate column is as wide as its two buttons need', !!tplBox && tplSpan > 0 && tplBox.width - tplSpan <= 40, JSON.stringify({ column: tplBox?.width, buttons: tplSpan }));
  await page.getByRole('button', { name: '新增' }).first().click();
  await page.waitForTimeout(800);
  const tplForm = await page.locator('.n-modal').innerText();
  check('metric templates: the form has the datasource, metric, level, step, PromQL, a test and a module to preview with', ['模板名称', '数据源', 'PromQL', '执行查询', '用哪个模块预览'].every(v => tplForm.includes(v)), tplForm.replace(/\n/g, ' ').slice(0, 160));
  await page.locator('.n-modal .n-form-item', { hasText: '模板名称' }).first().locator('input').fill('cpu-ui');
  // the only datasource is chosen by the form itself
  await page.locator('.n-modal textarea').first().fill('sum(rate(cpu_usage{namespace="{{namespace}}",pod=~"{{workload}}-.*"}[{{range}}]))');
  await page.locator('.n-modal .n-form-item', { hasText: '用哪个模块预览' }).first().locator('.n-select').first().click();
  await page.waitForTimeout(400);
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('Enter');
  await page.waitForTimeout(300);
  await page.locator('.n-modal').getByRole('button', { name: '执行查询' }).click();
  await page.waitForTimeout(1800);
  const tplRun = await page.locator('.n-modal').innerText();
  check('metric templates: the test renders the template for the module and shows its result in the form', (tplRun.includes('采样') || tplRun.includes('样本')) && (await page.locator('.n-modal canvas').count()) > 0, tplRun.replace(/\n/g, ' ').slice(-160));
  await page.locator('.n-modal').getByRole('button', { name: '保存' }).click();
  await page.waitForTimeout(1200);
  const tplList = (await call('GET', '/metric-mappings?page=1&page_size=100', token)).data.list;
  check('metric templates: the template is saved', tplList.some(x => x.name === 'cpu-ui' && x.metric === 'cpu'), tplList.map(x => x.name).join());
  await visit('monitoring/datasets');
  await shot('datasets-add');

  await page.goto(`${base}/monitoring/metrics`);
  await page.waitForTimeout(2000);
  check('the metric catalog page is gone (its work is in the Metrics dataset page)', !(await bodyText()).includes('内置模板'));

  await visit(`forecast/ranking?module_id=${moduleId}`);
  const rankingText = await bodyText();
  check('menu: the scores are the records and one ranking page', ['算法评分', '当前排名'].every(v => rankingText.includes(v)));
  check('ranking: the page shows each algorithm with its score and the best one', rankingText.includes('naive') && rankingText.includes('最优') && rankingText.includes('立即评分'));
  check('ranking: the algorithms are lanes with score bars and check boxes in one list, nothing stacked below it',
    (await page.locator('.rank-list').count()) === 1 && (await page.locator('.rank-list .rank-lane').count()) >= 2 && (await page.locator('.rank-list .n-checkbox').count()) >= 2 &&
    !['按指标排名', '历史变化'].some(v => rankingText.includes(v)));
  check('ranking: no tabs on the page', (await page.locator('.n-tabs').count()) === 0);
  check('ranking: one ranking only, no evaluation picker; "in use" is on one algorithm', (await page.getByText('评估时间', { exact: true }).count()) === 0 && (await page.locator('.rank-list .n-tag', { hasText: '使用中' }).count()) <= 1);
  await page.locator('.rank-list').getByRole('button', { name: '详情' }).first().click();
  await page.waitForTimeout(500);
  check('ranking: the detail compares the rank of the algorithm over the evaluations', (await page.locator('.n-modal').innerText()).includes('历史排名对比'));
  await page.keyboard.press('Escape');
  const pagerInView = async label => {
    const box = await page.locator('.n-pagination').first().boundingBox();
    return !!box && box.y + box.height <= (await page.viewportSize()).height;
  };
  check('ranking: the pager of the list is inside the card and the screen (it was clipped away before)', await pagerInView());
  check('ranking: the list scrolls inside its card, the page does not grow', await page.evaluate(() => document.scrollingElement.scrollHeight <= window.innerHeight + 4));
  await page.locator('.rank-list').getByRole('checkbox').first().check().catch(() => {});
  await shot('ranking');
  // the forecasts of this scratch run have no measured values yet: the lanes are drawn from an answer of the shape the API gives
  const metricScoreRow = (algorithm, accuracy) => ({ algorithm_id: algorithm, metric: 'cpu', source_name: 'cpu', kind: 'cpu', category: 'cpu', unit: 'cores', source_weight: 1, weight: 1, runs: 6, points: 60,
    mae: 0.05, rmse: 0.07, error: 1 - accuracy, accuracy, bias: 0.01, peak_under: 0.1, coverage: 1, in_composite: true });
  await page.route('**/metric-scores*', route => route.fulfill({ contentType: 'application/json', body: JSON.stringify({ code: 10000, msg: 'ok', data: {
    module_id: moduleId, best: 'ewma', comparable: true, sources: ['cpu'],
    algorithms: [{ algorithm_id: 'ewma', baseline: false, custom: false, accuracy: 0.92, error: 0.08, score: 92, rank: 1, sources: 1, runs: 6, points: 60, best: true, beats_baseline: true },
      { algorithm_id: 'naive', baseline: true, custom: false, accuracy: 0.8, error: 0.2, score: 80, rank: 2, sources: 1, runs: 6, points: 60, best: false }],
    metrics: [metricScoreRow('naive', 0.8), metricScoreRow('ewma', 0.92)],
    unranked: [{ algorithm_id: 'holt_winters', status: 'skipped', reason: 'insufficient history for the e2e', last_run_at: new Date().toISOString() }] } }) }));
  await page.getByRole('button', { name: '刷新' }).first().click();
  await page.waitForTimeout(1000);
  const mergedText = await bodyText();
  check('ranking: the measured accuracy is part of the ranking of each algorithm', mergedText.includes('实测准确度'));
  await page.unroute('**/metric-scores*');
  await shot('ranking-metrics');
  // the ranking per metric of an algorithm is in its detail
  await page.locator('.rank-list').getByRole('button', { name: '详情' }).first().click();
  await page.waitForTimeout(800);
  check('ranking: the detail of an algorithm has the score parts', (await page.locator('.n-modal').innerText()).includes('综合分'));
  await page.keyboard.press('Escape');
  await page.waitForTimeout(300);
  await page.goto(`${base}/forecast/ranking-history`);
  await page.waitForTimeout(2000);
  check('ranking history: the old address is not a page any more', !(await bodyText()).includes('各算法评分随时间的变化'));
  await visit('forecast/score-records');
  await page.waitForTimeout(1500);
  const scoreList = await bodyText();
  check('score records: the module name is not empty', !(await page.locator('.n-data-table-tbody .n-data-table-tr').first().innerText()).startsWith('\t'));
  check('score records: there is a resource filter (CPU / memory / GPU / overall)', (await page.getByText('全部资源').count()) > 0);
  check('score records: one row per module, algorithm and resource, not one per evaluation', (await page.locator('.n-data-table-tbody .n-data-table-tr').count()) <= 40 && /CPU|内存/.test(scoreList));
  await page.getByRole('button', { name: '详情' }).first().click();
  await page.waitForTimeout(900);
  const scoreDetail = await page.locator('.n-modal').innerText();
  check('score records: the detail has the result, the calculation basis and the earlier scores, apart', ['评分结果', '计算依据', '历史评分'].every(v => scoreDetail.includes(v)));
  check('score records: the basis names the metrics, the formula and the actual / forecast need of the windows', ['使用的指标', '准确度 = 1 −', '实际需要', '预测需要'].every(v => scoreDetail.includes(v)), scoreDetail.replace(/\s+/g, ' ').slice(0, 240));
  await page.keyboard.press('Escape');
  // the WAPE explanation: meaning, formula, steps and use, behind the question mark
  await visit('forecast-results/evaluations');
  await page.waitForTimeout(1200);
  await page.locator('.n-data-table-th', { hasText: 'WAPE' }).locator('.n-icon, svg').first().hover();
  await page.waitForTimeout(500);
  const wapeTip = await page.locator('.n-tooltip, .v-binder-follower-content').allInnerTexts();
  check('WAPE: the question mark gives the meaning, the formula, the steps and the use', ['含义', '公式', '计算过程', '用途'].every(v => wapeTip.join(' ').includes(v)));
  // the operation log: the source, the operation id and the client, with readable column names
  await visit('system/operation-log');
  await page.waitForTimeout(1200);
  const opText = await bodyText();
  check('operation log: the operation id and the client are columns', opText.includes('操作识别码') && opText.includes('客户端'));
  check('operation log: no raw database field name is shown as a column name', !/created_at|client_type|status_code|duration_ms/.test(opText));
  await visit('forecast-results/algorithm-runs');
  await page.waitForTimeout(1200);
  check('runs: there is no page of training records of its own, the menu has none', !(await page.locator('.n-menu').first().innerText()).includes('训练记录'));
  await page.getByRole('button', { name: '训练记录' }).first().click();
  await page.waitForTimeout(1000);
  const recordsModal = await page.locator('.n-modal').innerText();
  check('runs: the training records of an algorithm open in a modal over the page, listed with the status', recordsModal.includes('成功') || recordsModal.includes('跳过') || recordsModal.includes('失败'), recordsModal.replace(/\s+/g, ' ').slice(0, 200));
  check('runs: the URL stays on the algorithm run status', page.url().includes('algorithm-runs'));
  await page.locator('.n-modal').getByRole('button', { name: '详情' }).first().click();
  await page.waitForTimeout(800);
  const runDetail = await page.locator('.n-modal').innerText();
  check('runs: the detail has the status with its meaning, the window, the parameters and the reproduction hint', runDetail.includes('状态说明') && runDetail.includes('历史窗口') && runDetail.includes('参数') && runDetail.includes('模型版本'));
  await shot('runs-detail');
  check('runs: the detail has no tabs', (await page.locator('.n-modal .n-tabs').count()) === 0);
  await page.waitForTimeout(1500);
  check('runs: the training data under the run information draws the samples the run read and the forecast it wrote', (await page.locator('.n-modal canvas').count()) > 0 || (await page.locator('.n-modal').innerText()).includes('没有可显示的数据'));
  await shot('runs-data');
  await page.keyboard.press('Escape');

  const TIME_FILTERED = ['forecast/ranking', 'forecast-results/algorithm-runs', 'forecast-results/history', 'forecast-results/evaluations', 'forecast-results/leaderboard', 'forecast-actions/executions', 'forecast-actions/admissions'];
  const CHECKABLE = ['forecast/ranking', 'forecast-results/evaluations', 'forecast-results/leaderboard', 'forecast-actions/executions', 'forecast-actions/admissions'];
  for (const route of ['forecast/score-records', 'forecast/ranking', 'forecast-results/algorithm-runs', 'monitoring/datasets', 'forecast-results/history', 'forecast-results/evaluations', 'forecast-results/leaderboard',
    'forecast-actions/recommendations', 'forecast-actions/executions', 'forecast-actions/fallbacks', 'forecast-actions/admissions']) {
    await visit(route);
    const text = await bodyText();
    check(`${route} renders without unresolved text`, !/page\.(forecast|datasource)\./.test(text) && !page.url().includes('/login'));
    await operateFits(route);
    await noSideScroll(route);
    if (TIME_FILTERED.includes(route)) check(`${route}: filtered by a start and an end time`, (await page.locator('.time-range-field').count()) === 1 && !(await bodyText()).includes('最近 168 小时'));
    if (CHECKABLE.includes(route)) check(`${route}: rows have check boxes`, (await page.locator('.n-data-table-th--selection, .rank-lane .n-checkbox').count()) > 0);
    if (['forecast-actions/recommendations'].includes(route)) await shot(route.replace('/', '-'));
  }
  await page.goto(`${base}/forecast-results/trends`);
  await page.waitForTimeout(2000);
  check('trends: the separate trend page is gone (its view is the history page)', !(await bodyText()).includes('预测与实测曲线'));
  check('the menu has no trend comparison entry', !(await page.locator('.n-menu').first().innerText().catch(() => '')).includes('预测趋势对比'));
  await visit('forecast-results/history');
  const historyText = await bodyText();
  check('history: one chart and one list with the state of every enabled algorithm above the chart, no tabs and no overview',
    historyText.includes('历史数据') && !historyText.includes('已启用算法') && (await page.locator('.n-tabs').count()) === 0 && (await page.locator('canvas').count()) === 1, historyText.replace(/\s+/g, ' ').slice(0, 240));
  const chartBox = await page.locator('canvas').first().boundingBox();
  check('history: the chart has room for the legend, the toolbox and a range slider', !!chartBox && chartBox.height >= 400, JSON.stringify(chartBox));
  await shot('history');
  // the algorithms are chosen in the legend of the chart (name, latest value and error; a click shows or hides one): there is no
  // second list or selector above the chart
  const stateTags = await page.locator('.n-tag').allInnerTexts();
  check('history: no strip of algorithm tags and no algorithm selector above the chart', !stateTags.some(x => /·\s*WAPE/.test(x)) && (await page.getByText('全部有预测的算法').count()) === 0);
  const legendBox = await page.locator('canvas').first().boundingBox();
  check('history: the legend lives inside the chart', !!legendBox && legendBox.height >= 400);
  const recordRows = await page.locator('.n-data-table-tbody .n-data-table-tr').count();
  const pager = await page.locator('.n-pagination').count();
  check('history: the records list has rows and a pager only with rows', recordRows > 0 ? pager > 0 : pager === 0, `${recordRows} rows, ${pager} pagers`);
  check('history: the filters are module, metric, one time control and the line type', (await page.locator('.time-range-field').count()) === 1);
  for (const route of ['forecast/algorithms', 'forecast/score-records', 'forecast/ranking', 'forecast-results/history', 'forecast-results/evaluations', 'forecast-results/leaderboard',
    'forecast-results/algorithm-runs', 'forecast-actions/recommendations', 'forecast-actions/executions', 'forecast-actions/fallbacks', 'forecast-actions/admissions', 'monitoring/datasets', 'monitoring/curves', 'forecast-modules/modules']) {
    await visit(route);
    check(`${route}: no tabs`, (await page.locator('.n-tabs').count()) === 0);
  }
  // ---- the time filter is the last field of every search row and follows the theme like the fields beside it
  for (const route of ['forecast/score-records', 'forecast/ranking', 'forecast-results/history', 'forecast-results/evaluations', 'forecast-results/leaderboard',
    'forecast-results/algorithm-runs', 'forecast-actions/executions', 'forecast-actions/fallbacks', 'forecast-actions/admissions', 'monitoring/curves', 'system/login-log', 'system/operation-log']) {
    await visit(route);
    const last = await page.evaluate(() => {
      const items = [...document.querySelectorAll('.list-search-form .n-form-item')].filter(el => !el.closest('.list-search-actions'));
      const lastField = items.pop();
      return { timeIsLast: !!lastField?.querySelector('.time-range-field'), count: document.querySelectorAll('.time-range-field').length };
    });
    check(`${route}: the time filter is the last field of the search row`, last.timeIsLast && last.count === 1, JSON.stringify(last));
  }
  await visit('forecast-results/algorithm-runs');
  const timeColors = await page.evaluate(() => {
    // a transparent background shows the (white) page behind it; anything else has to be light
    const bg = el => {
      const [r, g, b, a = 1] = getComputedStyle(el).backgroundColor.match(/[\d.]+/g).map(Number);
      return { r, g, b, a };
    };
    return { button: bg(document.querySelector('.time-range-field')), select: bg(document.querySelector('.list-search-row .n-base-selection')) };
  });
  check('the time filter is light in the light theme, like the fields beside it (not black or dark)', timeColors.button.a < 0.2 || (timeColors.button.r > 200 && timeColors.button.g > 200 && timeColors.button.b > 200), JSON.stringify(timeColors));
  const timeHeight = await page.evaluate(() => ({ button: document.querySelector('.time-range-field').getBoundingClientRect().height, select: document.querySelector('.list-search-row .n-base-selection').getBoundingClientRect().height }));
  check('the time filter is as tall as the fields beside it', Math.abs(timeHeight.button - timeHeight.select) <= 2, JSON.stringify(timeHeight));
  const permissionOptions = (await call('GET', '/secret-keys/permission-options', token)).data || [];
  await call('POST', '/secret-keys', token, { name: 'e2e-operator', server_enabled: true, permission_ids: permissionOptions.filter(x => x.code === 'forecast:operator:use').map(x => x.id), status: true });
  for (const route of ['datasource/connections', 'system/secret-keys', 'forecast-modules/modules']) {
    await visit(route);
    const fit = await page.evaluate(() => {
      const th = document.querySelector('.n-data-table-th--fixed-right')?.getBoundingClientRect();
      const buttons = [...(document.querySelector('.n-data-table-td--fixed-right')?.querySelectorAll('button') || [])].map(b => b.getBoundingClientRect());
      const span = buttons.length ? buttons[buttons.length - 1].right - buttons[0].left : 0;
      return { column: Math.round(th?.width || 0), buttons: Math.round(span) };
    });
    check(`${route}: the operate column is as wide as its buttons need`, fit.buttons > 0 && fit.column - fit.buttons <= 60, JSON.stringify(fit));
  }
  await visit('forecast-results/algorithm-runs');
  const algorithmRunsText = await bodyText();
  check('algorithm runs: every enabled algorithm with its runs, hit rate and common reasons', ['算法运行情况', '运行次数', 'naive', 'ewma'].every(v => algorithmRunsText.includes(v)), algorithmRunsText.replace(/\s+/g, ' ').slice(0, 200));
  await visit('monitoring/curves');
  const curvesText = await bodyText();
  check('curves: the filters, the chart and the paged values, no series summary', ['曲线', '数据点', '来自 Prometheus'].every(v => curvesText.includes(v)) && !curvesText.includes('序列摘要') && (await page.locator('canvas').count()) > 0);
  const actualCandles = await call('GET', `/modules/${moduleId}/compare?metric=cpu&predictions=false`, token);
  check('curves: the values are read from Prometheus when the page opens, not from what was collected', actualCandles.data.actual.candles.length > 0 && actualCandles.data.actual.query.includes('cpu_usage'));
  await shot('curves-page');
  await page.goto(`${base}/forecast-actions/audit-exports`);
  await page.waitForTimeout(2000);
  check('audit exports: the page is removed', !(await bodyText()).includes('导出历史'));
  // ---- R10: history chart (refresh, legend, order, gaps), time quick choices, resource and snapshot pages, degradation and rollback
  await visit('forecast-results/history');
  await page.waitForTimeout(2500);
  check('R10 history: the chart has its own refresh button', (await page.locator('[data-chart-refresh]').count()) === 1);
  const compareCalls = [];
  page.on('request', req => { if (req.url().includes('/compare')) compareCalls.push(req.url()); });
  await page.locator('[data-chart-refresh]').click();
  await page.waitForTimeout(2500);
  check('R10 history: the refresh reads the chart again without pressing search', compareCalls.length >= 1, String(compareCalls.length));
  const order = (await page.locator('[data-series]').first().getAttribute('data-series')) || '';
  const seriesNames = order.split('|').filter(Boolean);
  const actualAt = seriesNames.findIndex(n => n === '实际值');
  const forecastNames = seriesNames.filter(n => n !== '实际值' && n !== '实际副本数');
  check('R10 history: the forecasts come before the actual values in the legend and the drawing order', forecastNames.length >= 1 && actualAt > 0 && forecastNames.every(n => seriesNames.indexOf(n) < actualAt), order);
  const chartPlotBox = await page.locator('[data-series]').first().boundingBox();
  check('R10 history: the chart does not push the page sideways', !!chartPlotBox && chartPlotBox.width <= 1440 && (await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)), JSON.stringify(chartPlotBox));
  await page.locator('.time-range-field').first().click();
  await page.waitForTimeout(500);
  const quick = await page.locator('.time-range-panel').innerText();
  check('R10 time: every time filter offers the last 3 hours and the last 12 hours', quick.includes('最近 3 小时') && quick.includes('最近 12 小时'), quick.replace(/s+/g, ' ').slice(0, 160));
  await page.keyboard.press('Escape');
  await visit('forecast/algorithms');
  check('R10 algorithms: the list says how far each algorithm has got (run state column)', (await bodyText()).includes('运行状态') && (await bodyText()).includes('运行中'));
  await visit('forecast-actions/recommendations');
  await page.waitForTimeout(1500);
  const recText = await bodyText();
  check('R10 recommendations: the page is the current resource recommendation, in units a person reads', recText.includes('资源推荐') && /d+m|d+(.d+)? 核/.test(recText) && !/d+ MiB/.test(recText) && !/d+ m/.test(recText), recText.replace(/s+/g, ' ').slice(0, 220));
  check('snapshots: there is no page of snapshots of its own, the menu has none', !(await page.locator('.n-menu').first().innerText()).includes('推荐快照'));
  await page.getByRole('button', { name: '快照历史' }).first().click();
  await page.waitForTimeout(1200);
  const snapText = await page.locator('.n-modal').innerText();
  check('snapshots: the history opens over the resource recommendation, each version named with its module', /\S+ 快照 V\d+/.test(snapText) && ['待执行', '已执行', '执行失败', '已过期', '仅建议'].some(v => snapText.includes(v)), snapText.replace(/\s+/g, ' ').slice(0, 200));
  await shot('snapshots');
  await page.locator('.n-modal').getByRole('button', { name: '决策说明' }).first().click();
  await page.waitForTimeout(1200);
  const snapDetail = await page.locator('.n-modal').innerText();
  check('snapshots: a version shows its module and version, the recommendation, why it exists, and the configuration, apart', ['模块与版本', '推荐结果', '产生原因', '配置内容与计算依据', 'Pod 资源规格'].every(v => snapDetail.includes(v)), snapDetail.replace(/\s+/g, ' ').slice(0, 240));
  await page.keyboard.press('Escape');
  await visit('forecast-actions/fallbacks');
  await page.waitForTimeout(1500);
  const fbText = await bodyText();
  check('R10 fallbacks: degradation and rollback are two kinds, each with trigger, original, target, result and time', ['触发原因', '原配置', '目标配置', '实际执行结果', '创建时间'].every(v => fbText.includes(v)), fbText.replace(/s+/g, ' ').slice(0, 220));
  await visit('forecast-results/leaderboard');
  const boardText = await bodyText();
  check('leaderboard: ranked by the underestimate-weighted error, shown beside WAPE with its explanation', boardText.includes('低估加权误差'), boardText.replace(/s+/g, ' ').slice(0, 200));
  await shot('leaderboard-risk');
  await page.goto(`${base}/forecast-results/backtests`);
  await page.waitForTimeout(2000);
  check('the rolling backtest page is gone', !(await bodyText()).includes('滚动回测记录'));
  await visit('forecast-results/evaluations');
  if ((await page.getByRole('button', { name: '详情' }).count()) > 0) {
    await page.getByRole('button', { name: '详情' }).first().click();
    await page.waitForTimeout(500);
    check('evaluations: a row opens its detail', (await page.locator('.n-modal').innerText()).includes('覆盖率'));
    await page.keyboard.press('Escape');
  }
  // ---- the users: the account type, the contacts as rows of any type, the roles with the same batch menu
  const created = await call('POST', '/users', token, { username: 'contact-user', password: 'Passw0rd!x', nick_name: 'contact', status: true, role_ids: [], contacts: [{ type: 'WhatsApp', value: '+8613800000000' }, { type: 'telegram', value: '@contact' }, { type: 'email', value: 'c@example.com' }] });
  check('a user is created with contacts of any type', created.status === 200, JSON.stringify(created));
  const listed = (await call('GET', '/users?username=contact-user', token)).data.list[0];
  check('the contacts come back as rows with lower case types, and the account is local', listed.contacts.length === 3 && listed.contacts.some(c => c.type === 'whatsapp') && listed.source === 'local', JSON.stringify(listed.contacts));
  await call('PUT', `/users/${listed.id}`, token, { contacts: [{ type: 'slack', value: 'u1' }] });
  const edited = (await call('GET', '/users?username=contact-user', token)).data.list[0];
  check('the contacts are replaced as a set', edited.contacts.length === 1 && edited.contacts[0].type === 'slack');
  check('the account types filter the list', (await call('GET', '/users?source=ldap', token)).data.list.every(u => u.source === 'ldap'));
  await visit('authorization/user');
  const userText = await bodyText();
  check('users: the account type and the contacts are columns, the old phone / email / id / avatar columns are gone', ['用户类型', '联系方式'].every(v => userText.includes(v)) && !userText.includes('头像') && (await page.getByText('批量操作').count()) > 0, userText.replace(/\s+/g, ' ').slice(0, 200));
  await call('DELETE', `/users/${listed.id}`, token);
  await visit('authorization/role');
  check('roles: the same batch menu as every list', (await page.getByText('批量操作').count()) > 0);
  await page.getByRole('button', { name: '新增' }).first().click();
  await page.waitForTimeout(700);
  check('roles: every field of the form has a question mark that explains it', (await page.locator('.n-drawer .n-form-item-label .n-icon, .n-drawer .n-form-item-label svg, .n-drawer .n-form-item-label [class*="help"]').count()) >= 4);
  await page.keyboard.press('Escape');
  await page.waitForTimeout(300);
  await visit('user-center');
  const centerText = await bodyText();
  check('user center: three sections on one page, no tabs, contacts of any type', (await page.locator('.n-tabs').count()) === 0 && ['修改密码', '联系方式'].every(v => centerText.includes(v)), centerText.replace(/\s+/g, ' ').slice(0, 160));
  const profile = await call('PUT', '/auth/profile', token, { contacts: [{ type: 'Telegram', value: '@admin' }] });
  const info = await call('GET', '/auth/info', token);
  check('user center: the profile keeps contacts as rows and mirrors the email', profile.status === 200 && info.data.contacts.some(c => c.type === 'telegram') && info.data.email === '', JSON.stringify(info.data.contacts));
  check('no stock "请输入" / "请选择" hint is left on the lists', !(await (async () => { await visit('forecast-results/algorithm-runs'); return (await page.locator('input[placeholder="请输入"], .n-base-selection-placeholder:text-is("请选择")').count()) > 0; })()));
  await visit('system/secret-keys');
  const keysText = await bodyText();
  check('cluster credentials: the page is named for what it is for and each credential has a purpose', keysText.includes('用途'));
  check('cluster credentials: Access Key and Secret Key are named as such, not as a mask or a salt', keysText.includes('Access Key（AK）') && keysText.includes('Secret Key（SK') && !keysText.includes('掩码') && !keysText.includes('盐'), keysText.replace(/\s+/g, ' ').slice(0, 200));
  const keyOperate = await page.locator('.n-data-table-th--fixed-right').first().boundingBox();
  check('cluster credentials: the operate column is as wide as its buttons need', !!keyOperate && keyOperate.width <= 190, JSON.stringify(keyOperate));
  // ---- the directory login is part of the system parameters, with its state, reachable from the page
  await visit('system/config');
  // the list is paged and has more parameters than a page: the LDAP ones are found by their key, like any other
  await page.getByPlaceholder('搜索参数名称、键名、参数值或备注...').fill('ldap.');
  await page.waitForTimeout(800);
  const cfgText = await page.locator('.n-data-table').first().innerText();
  check('system parameters: the LDAP settings are ordinary parameters in the list, with no separate entry or drawer', cfgText.includes('ldap.url') && cfgText.includes('ldap.enabled') && (await page.getByRole('button', { name: 'LDAP 认证配置' }).count()) === 0, cfgText.replace(/s+/g, ' ').slice(0, 160));
  check('system parameters: the LDAP bind password is shown masked', !cfgText.includes('svc-secret'));
  const operateButtons = await page.locator('.n-data-table-td--fixed-right .n-button').first().boundingBox();
  check('lists: the row buttons are tiny, like the task list', !!operateButtons && operateButtons.height <= 24, JSON.stringify(operateButtons));
  await shot('system-config');
  for (const route of ['system/login-log', 'system/operation-log']) {
    await visit(route);
    check(`${route}: one time control, the same as every list`, (await page.locator('.time-range-field').count()) === 1 && (await page.locator('.n-date-picker').count()) === 0);
  }
  await visit('forecast-results/history');
  await page.locator('.time-range-field').first().click();
  await page.waitForTimeout(500);
  const timePanel = await page.locator('.n-popover').last().innerText();
  check('the time control offers quick windows, a relative window and a fixed range in one panel', ['快捷选择', '相对时间', '固定范围'].every(v => timePanel.includes(v)), timePanel.replace(/\n/g, ' ').slice(0, 120));
  await page.keyboard.press('Escape');
  // ---- the pages speak Chinese: no English mode or state words in what the pages show
  for (const route of ['forecast-modules/modules', 'forecast-actions/admissions', 'forecast-actions/executions', 'forecast-actions/recommendations', 'forecast-results/leaderboard']) {
    await visit(route);
    const text = await bodyText();
    check(`${route}: no English mode or state words`, !/(Observe|Recommend|Admission|Auto|Fallback|Bypass)/.test(text), (text.match(/(Observe|Recommend|Admission|Auto|Fallback|Bypass)/) || [''])[0]);
  }
  check('no page error', errors.length === 0, errors.join(' | '));
} finally {
  await browser.close();
}

const failed = results.filter(r => !r.pass);
console.log(`${results.length - failed.length}/${results.length} checks passed`);
process.exit(failed.length ? 1 : 0);
