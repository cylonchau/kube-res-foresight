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

// ---- 1. algorithms: one setting per catalog algorithm, applied to every module ----------------------------------------
const algorithms = await call('GET', '/algorithms?page=1&page_size=100', token);
ok(algorithms, 'algorithms');
check('the catalog is listed with its settings and parameter schema', algorithms.data.total >= 14 && algorithms.data.list.every(a => 'enabled' in a && Array.isArray(a.params)));
check('the baselines start enabled and the heavy algorithms wait', algorithms.data.list.find(a => a.id === 'naive').enabled === true && algorithms.data.list.find(a => a.id === 'lstm').enabled === false);
check('the list is paged and searched', (await call('GET', '/algorithms?page=1&page_size=3', token)).data.list.length === 3 && (await call('GET', '/algorithms?keyword=holt', token)).data.total === 1);
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
ok(await call('POST', `/modules/${moduleId}/score`, token), 'score');
const ranking = await call('GET', `/modules/${moduleId}/ranking`, token);
check('the algorithms are ranked for the module', ranking.data.list.length >= 2 && ranking.data.list.every(r => r.score !== null), JSON.stringify(ranking.data.list.map(r => r.name)));
const rec = await call('POST', `/modules/${moduleId}/recommend`, token);
ok(rec, 'recommend');
check('the recommendation names the algorithm chosen', rec.data.recommendation.cpu_source === 'algorithm' && ['naive', 'ewma'].includes(rec.data.recommendation.algorithm), JSON.stringify(rec.data.recommendation).slice(0, 200));

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
  check('modules: the module is listed with its kind and status', (await bodyText()).includes('NGS APP') && (await bodyText()).includes('Deployment'));
  await operateFits('modules');
  const moduleButtons = await page.locator('.n-data-table-td--fixed-right').first().locator('button').allInnerTexts();
  check('modules: the operate column is one row of edit, dataset, delete and more', moduleButtons.map(x => x.trim()).join() === '编辑,数据集,删除,更多', moduleButtons.join());
  await page.getByRole('button', { name: '更多' }).first().click();
  await page.waitForTimeout(300);
  const moreMenu = await page.locator('.n-dropdown-menu').innerText();
  check('modules: the rarely used actions are in the more menu', ['克隆', '固定周期', '旁路'].every(v => moreMenu.includes(v)) && !moreMenu.includes('运行'), moreMenu.replace(/\n/g, ' '));
  await page.keyboard.press('Escape');
  check('modules: the list has check boxes and a batch menu', (await page.locator('.n-data-table-th--selection, .n-data-table-td--selection').count()) > 0 && (await bodyText()).includes('批量操作'));
  await page.getByRole('button', { name: '新增' }).first().click();
  await page.waitForTimeout(500);
  const form = await page.locator('.n-modal').innerText();
  check('module form: workload, data and forecast period, no combine / selection settings',
    form.includes('工作负载类型') && form.includes('历史窗口') && form.includes('预测周期') && form.includes('采样粒度') && form.includes('执行频率') && form.includes('最少数据量') && !form.includes('同一资源的合并方式') && !form.includes('最短历史') && !form.includes('算法评分'),
    form.replace(/\n/g, ' ').slice(0, 200));
  check('module form: the pace of the replica changes is configured on the module', ['副本调整节奏', '扩容单次最大步长', '缩容冷却'].every(v => form.includes(v)));
  check('module form: min, max and fallback replicas and the recovery are module settings, not part of the safety bounds', ['最小副本数', '最大副本数', '默认及故障回退副本数', '恢复启用所需周期数'].every(v => form.includes(v)) && !form.includes('副本上限') && !form.includes('副本下限'), form.replace(/\n/g, ' ').slice(0, 120));
  await shot('modules-form');
  await page.keyboard.press('Escape');

  await visit('forecast/algorithms');
  const algText = await bodyText();
  check('algorithms: the management page lists the algorithms with their enabled state', ['naive', 'ewma', 'lstm'].every(a => algText.includes(a)) && algText.includes('启用'));
  check('algorithms: no instance, binding or create-instance action', !algText.includes('创建实例') && !algText.includes('绑定'));
  check('algorithms: paged', (await page.locator('.n-pagination').count()) > 0);
  await page.getByPlaceholder('搜索算法名称或说明').fill('holt');
  await page.getByRole('button', { name: '搜索' }).last().click();
  await page.waitForTimeout(800);
  check('algorithms: the search narrows the list', (await page.locator('.n-data-table-tbody .n-data-table-tr').count()) === 1);
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
  await page.getByRole('button', { name: '算法需求' }).click();
  await page.waitForTimeout(1200);
  check('datasets: the requirements dialog says what each algorithm needs at this granularity', (await page.locator('.n-modal').innerText()).includes('naive') && (await page.locator('.n-modal').innerText()).includes('足够'));
  await page.keyboard.press('Escape');
  await page.waitForTimeout(400);
  await page.getByRole('button', { name: '数据周期' }).click();
  await page.waitForTimeout(500);
  const periodText = await page.locator('.n-modal').innerText();
  check('datasets: the period dialog offers the 5m, 15m, 1h, 1d and 30d presets', ['5m', '15m', '1h', '1d', '30d'].every(v => periodText.includes(v)), periodText.replace(/\n/g, ' ').slice(0, 200));
  await page.locator('.n-modal').getByRole('button', { name: '5m', exact: true }).first().click();
  await page.locator('.n-modal').getByRole('button', { name: '保存' }).click();
  await page.waitForTimeout(1200);
  check('datasets: the period is saved and validated against the rest', (await call('GET', `/modules/${grid.data.id}`, token)).data.step_seconds === 300);
  await page.keyboard.press('Escape');
  await page.getByRole('button', { name: '新增' }).first().click();
  await page.waitForTimeout(600);
  const addSource = await page.locator('.n-modal').innerText();
  check('datasets: adding a metric offers categories, a custom PromQL, scale, precision, aggregation and missing-data handling',
    ['分类', '自定义 PromQL', '换算系数', '精度', '聚合方式', '缺失处理'].every(v => addSource.includes(v)), addSource.replace(/\n/g, ' ').slice(0, 200));
  await page.getByRole('radio', { name: '自定义 PromQL' }).click().catch(() => {});
  await page.getByText('自定义 PromQL', { exact: true }).last().click().catch(() => {});
  await page.waitForTimeout(300);
  await page.locator('.n-modal').getByRole('button', { name: '测试查询' }).first().click();
  await page.waitForTimeout(800);
  const testerText = await page.locator('.n-modal').last().innerText();
  check('datasets: a PromQL is tested in a dialog with the query, the unit, the precision and the range', ['测试 PromQL 查询', '执行查询', '换算系数', '精度', '时间范围'].every(v => testerText.includes(v)), testerText.replace(/\n/g, ' ').slice(0, 160));
  await shot('query-test');
  await page.keyboard.press('Escape');
  await shot('datasets-add');
  await page.keyboard.press('Escape');

  await visit('monitoring/metrics');
  const catalogText = await bodyText();
  check('monitoring: the catalog lists the mappings of the modules, the built-in templates and the platform exports in one place', ['数据映射', '内置模板', 'cpu-workload'].every(v => catalogText.includes(v)) && (await page.locator('canvas').count()) === 0, catalogText.replace(/\s+/g, ' ').slice(0, 200));
  await operateFits('catalog');
  await page.getByRole('button', { name: '新增' }).first().click();
  await page.waitForTimeout(800);
  const mappingForm = await page.locator('.n-modal').innerText();
  check('catalog: a Prometheus metric can be added with a generated or hand-written PromQL', ['数据源', 'PromQL 模板（自动生成）', 'PromQL 模板', '采样步长'].every(v => mappingForm.includes(v)), mappingForm.replace(/\n/g, ' ').slice(0, 200));
  await page.locator('.n-modal textarea').first().fill('sum(up)');
  check('catalog: the PromQL can be typed by hand', (await page.locator('.n-modal textarea').first().inputValue()) === 'sum(up)');
  await page.keyboard.press('Escape');
  await page.waitForTimeout(300);
  check('catalog: the rows can be checked for a batch and a mapping can be tested', (await page.locator('.n-data-table-th--selection').count()) > 0 && (await bodyText()).includes('测试查询'));
  await shot('catalog');

  await visit(`forecast/ranking?module_id=${moduleId}`);
  const rankingText = await bodyText();
  check('menu: the scores are split into the records, the current ranking and the ranking history', ['算法评分', '当前排名', '历史排名'].every(v => rankingText.includes(v)));
  check('ranking: the current ranking shows each algorithm with its score and the best one', rankingText.includes('naive') && rankingText.includes('最优') && rankingText.includes('立即评分'));
  check('ranking: the algorithms are lanes with score bars, not a table', (await page.locator('.rank-lane').count()) >= 2 && (await page.locator('.n-data-table').count()) === 0 && (await page.locator('.rank-list').count()) === 1 && (await page.locator('.rank-list .n-checkbox').count()) >= 2);
  await shot('ranking');
  await page.getByText('指标评分（对比 Prometheus 实际值）').click();
  await page.waitForTimeout(1200);
  check('ranking: without measured values the metric scores say so, without a table', (await bodyText()).includes('还没有实测评估') && (await page.locator('.n-data-table').count()) === 0);
  // the forecasts of this scratch run have no measured values yet: the lanes are drawn from an answer of the shape the API gives
  const metricScoreRow = (algorithm, accuracy) => ({ algorithm_id: algorithm, metric: 'cpu', source_name: 'cpu', kind: 'cpu', category: 'cpu', unit: 'cores', source_weight: 1, weight: 1, runs: 6, points: 60,
    mae: 0.05, rmse: 0.07, error: 1 - accuracy, accuracy, bias: 0.01, peak_under: 0.1, coverage: 1, in_composite: true });
  await page.route('**/metric-scores*', route => route.fulfill({ contentType: 'application/json', body: JSON.stringify({ code: 10000, msg: 'ok', data: {
    module_id: moduleId, best: 'ewma', comparable: true, sources: ['cpu'],
    algorithms: [{ algorithm_id: 'ewma', baseline: false, custom: false, accuracy: 0.92, error: 0.08, score: 92, rank: 1, sources: 1, runs: 6, points: 60, best: true, beats_baseline: true },
      { algorithm_id: 'naive', baseline: true, custom: false, accuracy: 0.8, error: 0.2, score: 80, rank: 2, sources: 1, runs: 6, points: 60, best: false }],
    metrics: [metricScoreRow('naive', 0.8), metricScoreRow('ewma', 0.92)] } }) }));
  await page.getByRole('button', { name: '刷新' }).first().click();
  await page.waitForTimeout(1000);
  check('ranking: the composite result and the standard metric ranking are two separate lists with check boxes, without a table',
    (await page.locator('.rank-list').count()) === 2 && (await page.locator('.rank-list .rank-lane').count()) === 4 && (await page.locator('.rank-list .n-checkbox').count()) >= 4 && (await page.locator('.n-data-table').count()) === 0 &&
    (await page.locator('.rank-list').first().locator('.rank-lane__name').first().innerText()) === 'ewma');
  await page.unroute('**/metric-scores*');
  await shot('ranking-metrics');
  await visit('forecast/ranking-history');
  check('ranking history: the evaluations are listed with the order of the algorithms', (await bodyText()).includes('#1'));
  await page.getByRole('button', { name: '详情' }).first().click();
  await page.waitForTimeout(600);
  await page.locator('.n-modal').getByRole('button', { name: '评分构成' }).first().click();
  await page.waitForTimeout(400);
  check('ranking history: the detail lists the algorithms as lanes and shows the parts of a score', (await page.locator('.n-modal .rank-lane').count()) >= 2 && (await page.locator('.n-modal').innerText()).includes('贡献分'));
  await page.keyboard.press('Escape');
  await visit('forecast/score-records');
  await page.getByRole('button', { name: '详情' }).first().click();
  await page.waitForTimeout(600);
  const scoreDetail = await page.locator('.n-modal').innerText();
  check('score records: the detail has the error, accuracy, weight and contribution of each part', ['误差', '准确度', '权重', '贡献分', '综合分'].every(v => scoreDetail.includes(v)));
  await page.keyboard.press('Escape');
  await visit(`forecast-results/runs`);
  check('runs: the training records are listed with the status', (await bodyText()).includes('成功'));
  const runsText = await bodyText();
  check('runs: the overview says how many runs there were, how each algorithm fared and why runs did not forecast', ['运行概览', '命中率', '各算法运行情况', '未产出预测的常见原因'].every(v => runsText.includes(v)), runsText.replace(/\s+/g, ' ').slice(0, 200));
  await operateFits('runs');
  await page.getByRole('button', { name: '详情' }).first().click();
  await page.waitForTimeout(600);
  const runDetail = await page.locator('.n-modal').innerText();
  check('runs: the detail has the window, the parameters and the reproduction hint', runDetail.includes('历史窗口') && runDetail.includes('参数') && runDetail.includes('模型版本'));
  await shot('runs-detail');
  await page.keyboard.press('Escape');

  const TIME_FILTERED = ['forecast/score-records', 'forecast/ranking', 'forecast/ranking-history', 'forecast-results/runs', 'forecast-results/history', 'forecast-results/backtests', 'forecast-results/evaluations', 'forecast-results/leaderboard', 'forecast-results/trends', 'forecast-actions/executions', 'forecast-actions/admissions'];
  const CHECKABLE = ['forecast/score-records', 'forecast/ranking-history', 'forecast-results/runs', 'forecast-results/backtests', 'forecast-results/evaluations', 'forecast-results/leaderboard', 'forecast-actions/executions', 'forecast-actions/admissions'];
  for (const route of ['forecast/score-records', 'forecast/ranking', 'forecast/ranking-history', 'forecast-results/runs', 'monitoring/datasets', 'forecast-results/history', 'forecast-results/backtests', 'forecast-results/evaluations', 'forecast-results/leaderboard',
    'forecast-results/trends', 'forecast-actions/recommendations', 'forecast-actions/executions', 'forecast-actions/fallbacks', 'forecast-actions/admissions', 'forecast-actions/audit-exports']) {
    await visit(route);
    const text = await bodyText();
    check(`${route} renders without unresolved text`, !/page\.(forecast|datasource)\./.test(text) && !page.url().includes('/login'));
    await operateFits(route);
    await noSideScroll(route);
    if (TIME_FILTERED.includes(route)) check(`${route}: filtered by a start and an end time`, (await page.locator('.n-date-picker').count()) > 0 && !(await bodyText()).includes('最近 168 小时'));
    if (CHECKABLE.includes(route)) check(`${route}: rows have check boxes`, (await page.locator('.n-data-table-th--selection, .rank-lane .n-checkbox').count()) > 0);
    if (['forecast-results/backtests', 'forecast-results/trends', 'forecast-actions/recommendations'].includes(route)) await shot(route.replace('/', '-'));
  }
  await visit('forecast-results/trends');
  const trendsText = await bodyText();
  check('trends: the chart and the point by point list, no forecast summary', ['预测与实测曲线', '逐时间点'].every(v => trendsText.includes(v)) && !trendsText.includes('预测摘要') && (await page.locator('canvas').count()) > 0, trendsText.replace(/\s+/g, ' ').slice(0, 160));
  check('trends: the filters are module, metric, algorithms and a start and end time', (await page.locator('.n-date-picker').count()) > 0 && (await page.getByText('全部有预测的算法').count()) > 0);
  await shot('trends');
  await visit('monitoring/curves');
  const curvesText = await bodyText();
  check('curves: the filters, the chart and the paged values, no series summary', ['曲线', '数据点', '来自 Prometheus'].every(v => curvesText.includes(v)) && !curvesText.includes('序列摘要') && (await page.locator('canvas').count()) > 0);
  const actualCandles = await call('GET', `/modules/${moduleId}/compare?metric=cpu&predictions=false`, token);
  check('curves: the values are read from Prometheus when the page opens, not from what was collected', actualCandles.data.actual.candles.length > 0 && actualCandles.data.actual.query.includes('cpu_usage'));
  await shot('curves-page');
  await visit('forecast-actions/audit-exports');
  const exportsText = await bodyText();
  check('audit exports: the page keeps the export history and the types are chosen in a dialog', exportsText.includes('导出历史') && !exportsText.includes('全部模块') && (await page.locator('.n-modal').count()) === 0);
  await page.getByRole('button', { name: '导出 CSV' }).first().click();
  await page.waitForTimeout(500);
  const exportModal = await page.locator('.n-modal').innerText();
  check('audit exports: the dialog offers the five types, the module and the range', ['资源推荐快照', '降级与回退记录', '扩缩容执行记录', '实测评估记录', '预测值', '全部模块'].every(v => exportModal.includes(v)), exportModal.replace(/\n/g, ' ').slice(0, 200));
  await shot('audit-export-dialog');
  await page.keyboard.press('Escape');
  await visit('forecast-results/backtests');
  check('results: the backtests of the algorithms are listed for the module', (await bodyText()).includes('naive') && (await bodyText()).includes('ewma'));
  await page.getByRole('button', { name: '详情' }).first().click();
  await page.waitForTimeout(600);
  const modalText = await page.locator('.n-modal').innerText();
  check('backtests: a row opens its detail with the metrics', ['选择要查看的指标', '精度', '覆盖率'].every(v => modalText.includes(v)));
  await page.keyboard.press('Escape');
  await visit('forecast-results/evaluations');
  if ((await page.getByRole('button', { name: '详情' }).count()) > 0) {
    await page.getByRole('button', { name: '详情' }).first().click();
    await page.waitForTimeout(500);
    check('evaluations: a row opens its detail', (await page.locator('.n-modal').innerText()).includes('覆盖率'));
    await page.keyboard.press('Escape');
  }
  check('no page error', errors.length === 0, errors.join(' | '));
} finally {
  await browser.close();
}

const failed = results.filter(r => !r.pass);
console.log(`${results.length - failed.length}/${results.length} checks passed`);
process.exit(failed.length ? 1 : 0);
