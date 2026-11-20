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

# Resource dashboard (`kube-res-foresight-resources.json`)

A second dashboard (uid `kube-res-foresight-resources`) for the resource dashboard page of the platform: the cluster, the nodes, a workload, its Pods and
containers, and the JVM. It asks Prometheus the **same queries as the pages** (they are the system parameters `query.*`; the defaults are in
`pkg/resourcequery`), so the two never show different numbers. It is also in the chart (`deploy/helm/kube-res-foresight/files/resources-dashboard.json`,
the second key of the same ConfigMap).

The file is **generated**: after changing a query or a panel run `go run ./pkg/grafana/gen`; `go test ./pkg/grafana` fails when a shipped file is not what the generator
writes. `GET /api/v1/grafana/dashboard` builds it from the queries as they are set now in the system parameters, so a dashboard you download after
editing a query has your query.

## Load it and link it

1. Import the file (Dashboards > New > Import) or let the chart's sidecar label load it, and pick the Prometheus data source.
2. In the platform's system parameters set `grafana.base_url` (for example `https://grafana.example.com`), `grafana.dashboard_uid` (the default is the uid above) and, for the
   links back, `platform.public_url`. A parameter is read with a 10 second cache. The pages show **no** "open in Grafana" link until `grafana.base_url` is set.
3. `cluster.name` is the default of the *cluster* variable and the `var-cluster` of every link.

## Variables

| Variable | Meaning |
|---|---|
| `datasource` | the Prometheus data source |
| `cluster` | the cluster name (custom variable, default `cluster.name`) |
| `namespace`, `workload`, `pod`, `node`, `container` | "All" is the regular expression `.*`, which is what the platform fills a variable with too |

## Rows

Cluster (nodes, ready, allocatable, requested, used), Nodes (per node: ready, used, allocatable, requested), Workload (requests, limits and used; replicas), Pods, Containers,
JVM (heap and non-heap used, committed and maximum; GC count and pause).

## Links in both directions

* Platform to Grafana: `/d/<uid>?orgId=1&var-cluster=…&var-namespace=…&var-workload=…&var-pod=…&var-node=…&var-container=…&from=…&to=…`; the window of the page is the time range.
* Grafana to the platform (only when `platform.public_url` is set): the dashboard link and the workload panels open `/forecast-results/overprovision?namespace=…&workload=…&source=grafana`.
  The page takes a namespace and a workload that name exactly one thing; "All" (`.*`) and several chosen values are ignored.

## What was checked, and what was not

The JSON is checked by tests (structure, panels inside the 24 columns and not overlapping, every variable a query uses is defined, every shared query is on a panel, the
shipped files equal the generator). The pages and the links were looked at in a real browser against a fake Prometheus. **It has not been loaded into a real Grafana**: the version
of your Grafana may differ from schemaVersion 39, and the default metric names in the queries are defaults of editable parameters, not a promise that your Prometheus has them.
