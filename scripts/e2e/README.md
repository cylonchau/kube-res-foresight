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
