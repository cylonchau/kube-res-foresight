# Deploying the forecasting stack

Reference notes for the pieces added in P1-P6. None of the cluster-facing manifests here has been applied to a real
cluster: verify each step in a test cluster first (see "What is verified" below).

## Components

| Component | Runs as | Needs |
|---|---|---|
| Platform (`kube-res-foresight server`, or `all`) | one or more replicas | database, Prometheus-compatible datasources |
| Job engine (`kube-res-foresight job`, or inside `all`) | one process per database | same database; runs `forecast.run`, `forecast.backtest`, `forecast.evaluate`, `ldap.sync` |
| kube-res-foresight-operator (image target `operator`) | Helm chart `deploy/helm/kube-res-foresight-operator` (RBAC, Deployment, metrics) or the plain `deploy/operator/` manifests | cluster access only; reads decisions from the platform over HTTPS |
| Kyverno policy | `deploy/kyverno/kube-res-foresight-requests.yaml` | Kyverno; **reference draft, not verified** |

Platform metrics: set `metrics.listen` (for example `127.0.0.1:9100`) to serve `/metrics`, `/healthz` and `/readyz`.
`kube_res_foresight_*` series are labelled only by module, metric, algorithm, resource, source, state and datasource. The
metric catalog (Forecast > Metrics, `GET /api/v1/forecast-metrics/catalog`) is the single source of what exists: the
query API (`/api/v1/forecast-metrics/query`), the Metrics page and the exported series all derive from it, and a test
asserts that they agree. Besides the state gauges, `kube_res_foresight_collect_*`, `kube_res_foresight_predict_*`,
`kube_res_foresight_backtest_*` and `kube_res_foresight_evaluation_*` count and time the pipeline work, and
`kube_res_foresight_datasource_up` / `_latency_seconds` report the last probe of each datasource.

Operator metrics (`kube_res_foresight_operator_*`, same endpoint as the controller-runtime metrics, port 8080 by default):
reconciles by result and their duration, scale actions by direction and result, desired / current / ready replicas per
module, decision validity and time to expiry, conflicts by source (`hpa`, `keda`) and the requests
to the platform. `deploy/operator/monitoring/metrics-service.yaml` has a Service and a ServiceMonitor.

## Where the data lives

| Data | Table | Written by | Kept |
|---|---|---|---|
| Collected history | `metric_sample`, `metric_series` | `forecast.run` (collect) | the module's `lookback_hours` |
| Predictions (append only) | `forecast_prediction` | `forecast.run` (predict) | 30 days |
| Backtests | `backtest_run` | `forecast.backtest` | 180 days |
| Measured accuracy | `prediction_evaluation` | `forecast.evaluate` | 180 days |
| Recommendations | `recommendation` | `forecast.run` / manual | 180 days; the newest per module is always kept |
| Scaling reports | `scaling_execution` | operator | 90 days |
| Login history | `login_audit_log` | every login attempt | `sys.login_log.retention_days` (90) |
| Operation history | `operation_log` | every mutating request | `sys.operation_log.retention_days` (90) |

## Replica count: fixed or dynamic

A module's *replica calculation* is `fixed` (ceil(peak x headroom / capacity), the original formula, unchanged) or
`dynamic`: for every algorithm that has an eligible forecast the replicas are solved against throughput, CPU and memory
(per-pod requests x target utilization), the forecast peak is scaled by `1 + u` where `u` comes from that algorithm's
own measured peak shortfall (P90 of recent evaluations, else its latest backtest, else the module headroom, which the
page labels "no measurement"), the largest constraint wins, bounds apply, and a decline is only recommended when the
forecast confirms it for several steps. The pod capacity is entered by hand or estimated from history (needs the
replicas, rps and cpu mappings and the pod CPU request; with too few busy samples it refuses and says so). The
explanation of every number is stored in the recommendation detail. CPU and memory constraints need workload-level
mappings (the total of all pods). The estimated capacity has **not** been compared with a load test: treat it as
unverified until it has been.

## Operator decision source

The only source is the platform: the recommendation of a module, read over HTTP (`GET /api/v1/operator/decisions`). The operator keeps
no configuration of its own in the cluster: the workloads and their bounds come from the modules (`GET /api/v1/operator/modules`) and
its time limits from the platform's system parameters (`GET /api/v1/operator/settings`). The Adapter source (external metrics API) is
not offered on purpose: this operator replaces HPA/KEDA.

## Order of rollout

1. Migrate (`kube-res-foresight server --migration`, or `--upgrade` on an existing database). Normal start-up never migrates.
2. Register a datasource and metric mappings (UI: Data sources). Test the connection.
3. Create a forecast module in **Observe** mode, bind algorithms, run *Collect*, check *History and gaps*.
4. Run *Backtest* and compare with the baseline; promotion needs a strictly better WAPE and no worse peak
   underestimation. Run *Predict* / wait for the scheduled jobs; *Evaluate* fills in actuals as they arrive.
5. Switch the module to **Recommend** and review the recommendations and their explanations.
6. Only then consider the modes that write to the cluster. They are **closed by two system settings**, both `false`:
   `forecast.allow_admission_mode` and `forecast.allow_auto_mode`. Set one to `true` only after the corresponding
   piece (Kyverno policy / operator) has been verified in the target cluster.

## Credentials for machines

Create *API credentials* (System > API credentials) and give each exactly one permission:

- operator: `Operator Decisions (read, report)`
- Kyverno: `Admission Recommendations (read)`
- exports: `Audit Export (read)`

The credential is sent as `Authorization: kube-res-foresight-AK <key>:<secret>`. LDAP routes are never reachable with one.

## Safety switches and rollback

- Per module: **Bypass** (reason required, audited) keeps every original value; admission and the operator both answer
  "do not apply". *Resume* undoes it.
- Per mode: set `forecast.allow_admission_mode` or `forecast.allow_auto_mode` back to `false`. The admission and operator
  endpoints check the setting on every answer (it is cached for about ten seconds per process), so they stop applying
  at once even for modules still in that mode; modules can then be moved back to Recommend at leisure.
- Operator: move the module out of auto mode (or put it in bypass, or disable it) on the platform, or set `forecast.allow_auto_mode`
  to `false`; the operator never scales while a decision is missing or expired (it freezes, and a fallback can only raise replicas).
- One autoscaler per workload: an HPA or a KEDA ScaledObject on the same target makes the operator do nothing for that module and
  report a conflict.

## LDAP

Settings are in Authorization > LDAP. `ldaps://`, or `ldap://` with StartTLS; plain LDAP is refused. Directory users get
roles from group mappings at each login and at each hourly sync. Local accounts (the built-in `admin`) always use the
local password and keep working when the directory is down; keep at least one. Enabling LDAP requires a successful
connection check.

## Demo and presets

Data sources > Metric presets lists the built-in PromQL templates (cAdvisor, kube-state-metrics, ingress-nginx, Istio,
OOM) with the component each reads, the labels it needs and its known limits; applying one creates an ordinary mapping
that remembers its preset id. *Create demo* builds a `demo-synthetic` datasource (type `demo`, generated by the
program), the preset mappings and a `demo-shop` module, replays a few earlier prediction cycles and runs the whole
pipeline once, so every page has data without any Prometheus. The numbers are artificial and are labelled as such;
*Delete demo* removes only those objects.

## What is verified

Verified by automated tests and scratch-installation browser runs (`scripts/e2e/optimization.mjs`, `forecast.mjs`,
`platform.mjs`): the platform, its APIs, scheduled jobs, metrics, the pages, the demo, the LDAP client against a TLS
test server, and the operator reconcile logic and metrics against a fake Kubernetes client and fake Prometheus servers.
The `--upgrade` cleanup was run against a copy of a legacy schema.

Not verified: any real Prometheus (every preset is marked unverified), a real cluster (operator scaling,
RBAC, the metrics ServiceMonitor), Kyverno, a real LDAP server (StartTLS in particular), MySQL and
PostgreSQL, the dynamic replica solver and the estimated pod capacity against real load, and forecast accuracy on real
workloads.
