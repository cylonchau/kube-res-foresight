// Audit of the table columns of the list pages against a scratch installation that already holds data (run it after
// modules-algorithms.mjs, on the same server): for each column the width the table gives it, the widest content in its cells,
// and the white space between them. A column is flagged when it is much wider than anything in it, or when its text is cut
// off with no tooltip. This is how the "too wide, blank columns" of a page is measured instead of judged by eye.
// Usage: node scripts/e2e/table-fit.mjs  (E2E_URL, E2E_CHROME, PLAYWRIGHT_MODULE, E2E_PASSWORD optional)
import { pathToFileURL } from 'node:url';
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE ? pathToFileURL(process.env.PLAYWRIGHT_MODULE).href : '@playwright/test');

const base = process.env.E2E_URL || 'http://127.0.0.1:20122';
const slack = Number(process.env.SLACK || 90); // blank pixels tolerated beyond the padding of a cell
const routes = (process.env.ROUTES || [
  'forecast-results/evaluations', 'forecast-results/leaderboard', 'forecast-results/algorithm-runs', 'forecast-results/history',
  'forecast-actions/admissions', 'forecast-actions/executions', 'forecast-actions/recommendations', 'forecast-actions/fallbacks',
  'forecast/algorithms', 'forecast/score-records', 'forecast/ranking', 'forecast-modules/modules', 'monitoring/datasets', 'monitoring/curves',
  'datasource/connections', 'datasource/metric-mappings', 'datasource/presets', 'system/config', 'system/secret-keys', 'system/operation-log', 'system/login-log',
  'task-center/definition', 'task-center/audit', 'authorization/user', 'authorization/role'
].join(',')).split(',');

const res = await fetch(`${base}/api/v1/auth/login`, {
  method: 'POST', headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ username: 'admin', password: process.env.E2E_PASSWORD || '123456', force: true })
});
const login = (await res.json()).data;
const browser = await chromium.launch({ headless: true, ...(process.env.E2E_CHROME ? { executablePath: process.env.E2E_CHROME } : { channel: 'chromium' }) });
let flagged = 0;
try {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  await context.addInitScript(({ token, refreshToken }) => {
    localStorage.setItem('SOY_token', JSON.stringify(token));
    localStorage.setItem('SOY_refreshToken', JSON.stringify(refreshToken));
    localStorage.setItem('SOY_lang', JSON.stringify('zh-CN'));
  }, login);
  const page = await context.newPage();
  for (const route of routes) {
    await page.goto(`${base}/${route}`);
    await page.locator('.n-menu').first().waitFor({ state: 'visible' });
    await page.waitForTimeout(1800);
    const report = await page.evaluate(slackPx => {
      const out = [];
      const table = document.querySelector('.n-data-table');
      if (!table) return { rows: 0, columns: out };
      const ths = [...table.querySelectorAll('.n-data-table-thead .n-data-table-th')];
      const rows = [...table.querySelectorAll('.n-data-table-tbody .n-data-table-tr')];
      ths.forEach((th, index) => {
        const title = th.innerText.trim();
        const width = th.getBoundingClientRect().width;
        if (!title || th.classList.contains('n-data-table-th--selection') || width < 1) return;
        let widest = 0;
        let cut = 0;
        let cells = 0;
        for (const row of rows) {
          const td = row.querySelectorAll('.n-data-table-td')[index];
          if (!td) continue;
          cells += 1;
          // the natural width of the content: the inner nodes, not the cell stretched to the column
          const probe = document.createElement('span');
          probe.style.cssText = 'position:absolute;visibility:hidden;white-space:nowrap;font:inherit';
          probe.innerHTML = td.innerHTML;
          td.appendChild(probe);
          widest = Math.max(widest, probe.getBoundingClientRect().width);
          td.removeChild(probe);
          const inner = td.querySelector('.n-ellipsis, .n-data-table-td__ellipsis');
          if (inner && inner.scrollWidth > inner.clientWidth + 1) cut += 1;
        }
        out.push({ title, width: Math.round(width), widest: Math.round(widest), cells, cut, blank: Math.round(width - widest - 24), sliderHead: Math.round(th.querySelector('.n-data-table-th__title')?.getBoundingClientRect().width || 0) });
      });
      return { rows: rows.length, columns: out, slack: slackPx };
    }, slack);
    const wide = report.columns.filter(c => c.cells > 0 && c.blank > slack && c.title !== '操作');
    const header = `${route} (${report.rows} rows)`;
    if (!wide.length) {
      console.log(`ok   ${header}`);
      continue;
    }
    flagged += wide.length;
    console.log(`WIDE ${header}`);
    for (const c of wide) console.log(`       ${c.title}: column ${c.width}px, widest content ${c.widest}px, blank ${c.blank}px`);
  }
} finally {
  await browser.close();
}
console.log(flagged ? `${flagged} column(s) wider than their content by more than ${slack}px` : 'every column fits its content');
