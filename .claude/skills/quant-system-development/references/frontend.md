# Frontend

Use existing pages and shared Naive UI components. Keep the standard list layout:
search card, table toolbar, ExpandableDataTable and real API pagination. Reuse
column settings, copy helpers, status tags, operation buttons and error handling.
Lists offer adaptive, 30, 50, 100 and 200 row sizes. Labels are above form inputs.
Use field-specific placeholders and Chinese/English i18n for all visible text.
Hints use IconTooltip rather than permanent banners. Row actions stay horizontal.
Use the shared list-search plugin and useStatsStream for statistics. Do not add
page-specific polling or duplicate infrastructure. Verify routes and navigation,
run typecheck and build, and inspect dialogs in the browser when changing them.
