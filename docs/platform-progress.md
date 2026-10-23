# Platform simplification — 2026-10-23

Scope: Workflow Engine, System Management, Authorization Management, Task Center.
Removed pages, runtime wiring, APIs, collectors, strategies, data models, SDK dependencies
and locale groups for the retired business. Shared framework and UI conventions remain.
Default landing route: workflow_pipelines. The profile route stays hidden.

Existing database cleanup is explicit via --upgrade, with an inventory of retired tables.
Custom generic tasks, settings and permissions remain. No production database is upgraded automatically.
Validation: go test ./... passed; admin typecheck, locale tests and production build
passed. A scratch database initialized from all 131 existing table schemas upgraded
successfully to 49 tables with no retired business tables remaining. Playwright
verified all ten retained pages and the four visible menu roots without page errors;
removed business API endpoints returned 404. Screenshots were inspected.

The backend and existing nested admin Git repositories retain their original
layout. Each contains one main branch and one new initial commit, with old refs
and unreachable objects removed using Git's own history management commands.
No remote rewrite.

Directory cleanup: removed retired CLI, SSL deployment/provider, plugin and browser
exchange-client empty directories, 43 obsolete design/reference/progress documents,
and four retired trading executables. The original database and backups remain.
Recursive removal was rejected by the execution policy, so the remaining nested Git
metadata under refer/coredns_mysql and four old browser-profile cache directories
could not be removed. Source/document files were removed individually and empty
directories were removed without recursion.

Command cleanup: removed both standalone messaging-agent commands and their command
implementations, tests and binary entrypoint. The CLI exposes only all, platform,
server, job, notify and version (plus Cobra help/completion). Internal workflow
delivery adapters remain. The command-list regression test checks the complete
supported command set. Workflow persistence and internal delivery assembly now live
in pkg/app; cmd and pkg/cmd contain no messaging-provider names or dependencies.

Product identity: KubeForecast, focused on Kubernetes resource forecasting,
Pod scaling orchestration and capacity planning. Replaced the product name, SVG
logo, favicon, loading screen, login copy, CLI, Go module namespace, configuration
prefix, request headers, WebSocket protocol and deployment/build identifiers.
The four management modules remain the foundation; forecasting models, cluster
metrics ingestion and automatic Pod scaling are not implemented by this change.
Runtime databases and cryptographic key values are not changed by the rename.
Validation: Go tests, admin type checking, locale tests and production build passed.
Browser checks covered Chinese/English login, a 390px viewport, favicon delivery,
all ten management pages and the shared product title. Screenshots were inspected.
