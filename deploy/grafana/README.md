# Grafana dashboard

`kube-res-foresight-dashboard.json` is one dashboard (uid `kube-res-foresight`) over the metrics of the platform and the operator. It is
also in the chart (`deploy/helm/kube-res-foresight/files/dashboard.json`, a copy: change both, or copy this file over that one).

## Load it

* **Helm:** `--set metrics.grafanaDashboard.enabled=true` makes a ConfigMap with the label `grafana_dashboard=1`, which the sidecar of the
  Grafana in kube-prometheus-stack loads. Change `labelKey`, `labelValue` and `namespace` to what your sidecar looks for.
* **By hand:** Dashboards > New > Import > upload the file, and pick the Prometheus data source.

The dashboard asks for a Prometheus data source (variable *Data source*), and offers *Module* and *Forecast metric* filters.

## What Prometheus must scrape

| Job | Endpoint | Series |
|---|---|---|
| the platform | `metrics.listen` (the chart: port 9100, ServiceMonitor `metrics.serviceMonitor.enabled`) | `kube_res_foresight_*` |
| the operator | port 8080 (the operator chart: `metrics.serviceMonitor.enabled`) | `kube_res_foresight_operator_*` |

The operator rows stay empty until the operator is scraped.

## Rows

| Row | Panels | Kind |
|---|---|---|
| Overview | modules enabled, datasources down, stale recommendations, failing modules, failed jobs (24 h), operator errors | stat |
| History and trend | one panel per forecast metric (CPU, rps, replicas...): the collected value over time and the forecast peak by algorithm; recommended values; age of the newest sample and of the newest recommendation | time series |
| Algorithm ranking | leaderboard by composite score; error ranking (WAPE, lowest first); peak underestimation ranking; the algorithm in use; the newest backtest of every algorithm | bar gauges, tables |
| Algorithm runs | prediction runs by result and by algorithm; prediction time p95; collections; backtests; scored predictions and recommendations; task center jobs; fallbacks | time series |
| Scaling records and operator state | desired / current / ready replicas (operator) and as reported to the platform; scale actions by direction and result; reports of the last 24 h; the state of each module (normal, cached, holding, degraded, recovering); decision age and expiry; operator to platform requests; decision source and cache; report outbox; reconciles and conflicts | time series, bar gauge, table |

## Series added for this dashboard

On top of the series that already existed (`kube_res_foresight_usage_value`, `_prediction_*`, `_backtest_*`, `_algorithm_score`,
`_scaling_replicas`, ...) the platform now exports:

| Series | Labels | Meaning |
|---|---|---|
| `kube_res_foresight_scaling_actions_24h` | module, action, result | Scaling reports of the last 24 h, counted by action (ScaleUp, ScaleDown, None) and result. |
| `kube_res_foresight_scaling_last_report_age_seconds` | module | Age of the newest report. |
| `kube_res_foresight_scaling_degrade_state` | module, state | 1 for the state of the module at its newest report. |
| `kube_res_foresight_jobs` | type, status | Jobs of the task center created in the last 24 h. |

## What was checked, and what was not

Prometheus 2.55 and Grafana 11.3 in Docker, scraping a platform (SQLite, a synthetic datasource, one module with the full pipeline run) and
the operator of the local cluster. Every query of the dashboard was run against Prometheus and answered without error; panels whose series
exist rendered with data. Panels that stay empty in a healthy system, because the series appear only when something happens: backtests
(they run rarely), fallbacks of recommendations, operator conflicts, the report outbox and its drops, the decision source and the cache age
(the operator image in the cluster did not export the last two). The alert rules were checked with `promtool`. Not checked: a real,
loaded cluster over days, so the thresholds of the alerts and the colours of the ranking bars are starting points.
