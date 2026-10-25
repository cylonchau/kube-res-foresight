# Platform browser checks

Use a scratch database and configuration, run the explicit schema migration, build
the admin, then open the four modules with Playwright. The local `serve.mjs`
proxies the built SPA to the scratch API. Never point these checks at a live database.

`platform.mjs` verifies the four menu roots, removed API endpoints and all ten
retained pages, saving screenshots and a JSON report under target/platform-qa.
Install `@playwright/test` in a development environment and its Chromium browser;
set E2E_URL / E2E_OUTPUT as needed, then run `node scripts/e2e/platform.mjs`.
The default login is only for a newly migrated scratch database. Set E2E_PASSWORD
when using another scratch account. Never use this script against production.

## Multi-metric forecast pages

`multimetric.mjs` covers the module management menu (module list, metric sources) apart from forecast management
(algorithms and instances, scores and ranking), several sources per resource with a weighted combination, GPU, network and
custom metrics, algorithm instances made in the UI with parameters entered directly (no templates), scoring and ranking,
the low-score fallback reason, the admission records (an applied and a refused answer), the source-by-source explanation
of a recommendation with the OOM floor, the result and action pages, presets, and no horizontal overflow at 1440 / 1024 /
390 px. It needs a freshly migrated scratch database, a server on `E2E_URL`, and `fake-prometheus.mjs` on
127.0.0.1:19090 (it serves daily series plus an OOM kill, a 95% JVM heap, GPU, network and a queue-depth series).

## Optimization pages

`optimization.mjs` covers the pages added or reworked by the optimization work: presets and the one-click demo (it
creates `demo-synthetic` and `demo-shop`, so it needs no Prometheus), module management with the replica explanation,
history / evaluation / recommendations on the demo data, the algorithm center (instances, catalog, detail, trace, an instance made in the UI), the metrics page and its CSV export, the login log
and its filters, the LDAP page, the lockout and retention settings, and layout checks at 1440 / 1024 / 390 px plus a
short window. It expects a freshly migrated scratch database and a server on `E2E_URL` (default
http://127.0.0.1:20122) with `metrics.listen` on 127.0.0.1:20123. `E2E_CHROME` may point at a Chromium executable and
`PLAYWRIGHT_MODULE` at an installed `@playwright/test` (the other scripts take the same variables).
