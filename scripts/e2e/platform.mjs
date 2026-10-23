// Run against an explicitly prepared scratch installation only.
import assert from 'node:assert/strict';
import { mkdir, writeFile } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';

const { chromium, expect } = await import(process.env.PLAYWRIGHT_MODULE
  ? pathToFileURL(process.env.PLAYWRIGHT_MODULE).href : '@playwright/test');
const base = process.env.E2E_URL || 'http://127.0.0.1:20111';
const output = process.env.E2E_OUTPUT || 'target/platform-qa';
await mkdir(output, { recursive: true });
await expect.poll(async () => {
  try { return (await fetch(`${base}/ping`)).status; } catch { return 0; }
}, { timeout: 15000 }).toBe(200);
const login = await fetch(`${base}/api/v1/auth/login`, {
  method: 'POST', headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ username: 'admin', password: process.env.E2E_PASSWORD || '123456', force: true })
}).then(r => r.json());
assert.equal(login.code, 10000, JSON.stringify(login));
const headers = { Authorization: `Bearer ${login.data.token}` };
const routes = await fetch(`${base}/api/v1/route/get-user-routes`, { headers }).then(r => r.json());
assert.equal(routes.data.home, 'workflow_pipelines');
assert.deepEqual(routes.data.routes.filter(r => !r.meta.hideInMenu).map(r => r.name).sort(),
  ['authorization', 'system', 'task-center', 'workflow']);
for (const path of ['/market/tickers', '/scanner/status', '/trade/dashboard', '/collector/sources']) {
  assert.equal((await fetch(`${base}/api/v1${path}`, { headers })).status, 404);
}
const browser = await chromium.launch({ headless: true, channel: 'chromium' });
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
await context.addInitScript(({ token, refreshToken }) => {
  localStorage.setItem('SOY_token', JSON.stringify(token));
  localStorage.setItem('SOY_refreshToken', JSON.stringify(refreshToken));
  localStorage.setItem('SOY_lang', JSON.stringify('zh-CN'));
}, login.data);
const page = await context.newPage();
const errors = [];
const pending = new Set();
page.on('request', req => { if (req.url().includes('/api/')) pending.add(req); });
page.on('requestfinished', req => pending.delete(req));
page.on('requestfailed', req => pending.delete(req));
page.on('pageerror', err => errors.push(err.message));
const pages = ['workflow/pipelines', 'workflow/instances', 'system/config', 'system/operation-log', 'system/secret-keys',
  'authorization/user', 'authorization/role', 'authorization/menu', 'task-center/definition', 'task-center/audit'];
try {
  const loginContext = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  try {
    const loginPage = await loginContext.newPage();
    loginPage.on('pageerror', err => errors.push(err.message));
    for (const [width, lang] of [[1440, 'zh-CN'], [390, 'zh-CN'], [1440, 'en-US']]) {
      await loginPage.setViewportSize({ width, height: 1000 });
      await loginPage.goto(`${base}/login`);
      await loginPage.evaluate(language => localStorage.setItem('SOY_lang', JSON.stringify(language)), lang);
      await loginPage.reload();
      await expect(loginPage.locator('.login-brand-top')).toContainText('KubeForecast');
      await expect(loginPage.locator('.login-slogan')).toContainText('Kubernetes');
      await expect(loginPage.locator('.login-slogan')).toContainText('Pod');
      await expect(loginPage).toHaveTitle(/KubeForecast/);
      assert.ok(!/hermes/i.test(await loginPage.locator('body').innerText()));
      assert.ok(await loginPage.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), 'login overflows viewport');
      await loginPage.screenshot({ path: `${output}/brand-login-${width}-${lang}.png`, fullPage: true, animations: 'disabled' });
    }
  } finally { await loginContext.close(); }
  const logo = await fetch(`${base}/kubeforecast-logo.svg`);
  assert.equal(logo.status, 200);
  assert.match(await logo.text(), /KubeForecast/);
  for (const path of pages) {
    await page.goto(`${base}/${path}`);
    await page.locator('.n-menu').first().waitFor({ state: 'visible' });
    await page.locator('.n-data-table').first().waitFor({ state: 'visible' });
    await expect.poll(() => pending.size).toBe(0);
    await expect(page.locator('.n-data-table-loading-wrapper')).toHaveCount(0);
    assert.ok(!page.url().includes('/login'), `${path} redirected to login`);
    const text = await page.locator('body').innerText();
    assert.ok(text.includes('KubeForecast') && !/hermes/i.test(text), `product identity on ${path}`);
    assert.ok(!/量化|市场数据|链上雷达|策略中心|交易与风控/.test(text), path);
    assert.ok(!/page\.(workflow|system|authorization|task|job)\./.test(text), `unresolved locale key on ${path}`);
    await page.screenshot({ path: `${output}/${path.replaceAll('/', '-')}.png`, fullPage: true, animations: 'disabled' });
    console.log('Verified', path);
  }
  assert.deepEqual(errors, []);
  await writeFile(`${output}/browser-result.json`, JSON.stringify({ pages, roots: routes.data.routes.map(r => r.name), errors }, null, 2));
} finally { await browser.close(); }
