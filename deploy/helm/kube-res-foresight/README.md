# kube-res-foresight chart

Deploys the platform: the API and web console, the forecasting pipeline and the job engine (the image runs `kube-res-foresight all`),
with its metrics endpoint, an optional ServiceMonitor, alert rules and the Grafana dashboard.

The operator that scales the workloads is a chart of its own, [`kube-res-foresight-operator`](../kube-res-foresight-operator): install this
chart first, create the developer credential in the console, then install the operator.

## What was verified

Installed with Helm 3.16 on a Docker Desktop Kubernetes cluster, with an image built from `deploy/docker/Dockerfile`:

| Checked | Result |
|---|---|
| `helm lint`, `helm template` for sqlite, mysql and postgres, with every optional block on | pass |
| Install with PostgreSQL 16 (hook Job migrates, then the Deployment starts), sign-in, `helm test`, `helm upgrade`, `helm uninstall` | pass |
| Install with sqlite (init container migrates, PVC), restart of the Pod keeps the data | pass |
| The secret key is kept on upgrade | pass |
| ServiceMonitor picked up by the cluster's Prometheus, target `up` | pass |
| Alert rules with `promtool check rules` | pass |
| Grafana dashboard against a Prometheus and a Grafana in Docker | pass (see `deploy/grafana/README.md`) |

**Not verified:** MySQL (rendered only, not installed), more than one replica, Ingress with a real controller, a public image registry
(there is none), the upgrade from an older chart version (this is the first).

## Prerequisites

* Kubernetes 1.25+ and Helm 3.
* An image of the platform: `docker build -f deploy/docker/Dockerfile --target kube-res-foresight -t REGISTRY/kube-res-foresight:TAG .`
  and push it. There is no public image, so `image.repository` is required.
* For anything but a trial: a MySQL or PostgreSQL database and a user that may create tables in it.
* Optional: the Prometheus Operator CRDs (ServiceMonitor, PrometheusRule) and a Grafana with the dashboard sidecar.

## Install

Trial with sqlite on a volume:

```bash
helm install kube-res-foresight deploy/helm/kube-res-foresight -n kube-res-foresight --create-namespace \
  --set image.repository=REGISTRY/kube-res-foresight --set image.tag=TAG
```

With PostgreSQL (the schema is made by a hook Job before the Deployment starts):

```bash
helm install kube-res-foresight deploy/helm/kube-res-foresight -n kube-res-foresight --create-namespace \
  --set image.repository=REGISTRY/kube-res-foresight --set image.tag=TAG \
  --set database.driver=postgres --set database.host=pg.example.internal --set database.name=kube-res-foresight \
  --set database.username=kube-res-foresight --set secrets.databasePassword=...
```

Use `database.driver=mysql` the same way. Keep the passwords out of the command line with an existing Secret:

```bash
kubectl -n kube-res-foresight create secret generic kube-res-foresight-credentials \
  --from-literal=jwt-secret="$(openssl rand -hex 24)" \
  --from-literal=secret-key="$(openssl rand -hex 24)" \
  --from-literal=database-password='...'
helm install kube-res-foresight deploy/helm/kube-res-foresight ... --set secrets.existingSecret=kube-res-foresight-credentials
```

Then open the console (`kubectl port-forward svc/kube-res-foresight 8080:80`, or the Ingress). **The first sign-in is `admin` /
`123456`: change the password at once.** `helm test kube-res-foresight` checks `/readyz` and the metrics through the Service.

With monitoring (kube-prometheus-stack: the labels are the ones its Prometheus and Grafana select by):

```bash
helm upgrade kube-res-foresight deploy/helm/kube-res-foresight -n kube-res-foresight --reuse-values \
  --set metrics.serviceMonitor.enabled=true --set metrics.serviceMonitor.labels.release=prom \
  --set metrics.prometheusRule.enabled=true --set metrics.prometheusRule.labels.release=prom \
  --set metrics.grafanaDashboard.enabled=true
```

## Values

| Value | Default | Meaning |
|---|---|---|
| `image.repository` / `tag` / `pullPolicy` | `""` / chart appVersion / `IfNotPresent` | The platform image (required). |
| `replicaCount` | `1` | Every replica runs the API and the job engine; sqlite allows only 1. |
| `timezone` | `""` | Time zone of the times shown (`KUBE_RES_FORESIGHT_TIMEZONE`); empty is UTC+8. |
| `server.port` | `10000` | Port of the API and console in the Pod. |
| `server.corsAllowedOrigins` | `[]` | Origins allowed to call the API from another site. |
| `database.driver` | `sqlite` | `sqlite`, `mysql` or `postgres`. |
| `database.sqlite.file` | `/data/kube-res-foresight.db` | The sqlite file, on the `persistence` volume. |
| `database.host` / `port` / `name` / `username` | `""` / `0` / `kube-res-foresight` / `kube-res-foresight` | MySQL and PostgreSQL connection (port 0 is 3306 or 5432). |
| `database.sslMode` | `disable` | PostgreSQL SSL mode. |
| `database.maxOpenConnection` / `maxIdleConnection` | `100` / `50` | MySQL pool. |
| `secrets.existingSecret` | `""` | A Secret with the keys below; nothing is created when set. |
| `secrets.keys.*` | `jwt-secret`, `secret-key`, `database-password` | The key names inside the Secret. |
| `secrets.jwtSecret` / `secretKey` / `databasePassword` | `""` | When the chart makes the Secret. Empty `jwtSecret` and `secretKey` are generated once and kept on upgrade. |
| `migration.enabled` / `mode` | `true` / `auto` | `job` (hook Job), `initContainer`, or `auto` (init container for sqlite, Job otherwise). |
| `migration.resources` | 50m / 64Mi, limit 500m / 256Mi | Of the Job, the init container and the test Pod. |
| `persistence.*` | enabled, 5Gi, RWO | The volume of the sqlite file; `existingClaim`, `storageClass`, `size`. |
| `service.type` / `port` | `ClusterIP` / `80` | The console Service (the metrics port is on it too). |
| `ingress.*` | disabled | `className`, `annotations`, `hosts`, `tls`. |
| `gateway.*` | disabled | Gateway API: an HTTPRoute for an existing Gateway. `parentRefs` (required), `hostnames`, `path`, `pathType`, `timeouts`, `annotations`. Needs the Gateway API CRDs. |
| `metrics.port` | `9100` | `/metrics`, `/healthz`, `/readyz`; the probes use them. |
| `metrics.legacyNames` | `false` | Also export every series as `kube_resource_fcst_*` (the name before the rename), so alerts and dashboards of an earlier release keep working while they are moved. |
| `metrics.serviceMonitor.*` | disabled | `interval`, `labels`. |
| `metrics.prometheusRule.*` | disabled | Ten alerts; `labels`. |
| `metrics.grafanaDashboard.*` | disabled | ConfigMap for the Grafana sidecar: `labelKey`, `labelValue`, `namespace`, `labels`, `annotations`. |
| `extraConfig` | `{}` | Merged into `config.yaml` (for example `loggers`). |
| `extraEnv` | `[]` | Extra environment variables. |
| `resources` | 200m / 256Mi, limit 2 / 1Gi | Of the platform container. |
| `podSecurityContext`, `securityContext` | non-root 10001, read-only root filesystem, no capabilities | |
| `podDisruptionBudget.*`, `nodeSelector`, `tolerations`, `affinity`, `priorityClassName`, `podAnnotations`, `podLabels`, `serviceAccount.*`, `imagePullSecrets` | | The usual. |

The settings that live in the console (the e-mail channel `notify.email.*`, the AI assistant switch, forecast and operator parameters) are
system parameters in the database, not chart values.

## Upgrade

```bash
helm upgrade kube-res-foresight deploy/helm/kube-res-foresight -n kube-res-foresight --reuse-values --set image.tag=NEWTAG
```

The schema is migrated before the new version starts (the hook Job, or the init container). With sqlite the Deployment is `Recreate`
(one writer on the volume), so there is a short interruption; with a database and more than one replica the update is rolling.
Back up the database before an upgrade. The Secret is not touched: `jwtSecret` and `secretKey` stay the same, which matters, because a
changed `secretKey` makes the stored LLM keys and passwords unreadable.

`server --upgrade` (which drops retired tables of older versions) is **not** run by the chart: run it by hand when the release notes say so.

## Uninstall

```bash
helm uninstall kube-res-foresight -n kube-res-foresight
```

The Secret (the keys) and the sqlite volume claim are **kept** (`helm.sh/resource-policy: keep`), so that a reinstall finds its data.
Delete them when the data is not wanted any more:

```bash
kubectl -n kube-res-foresight delete pvc kube-res-foresight
kubectl -n kube-res-foresight delete secret kube-res-foresight
```

A MySQL or PostgreSQL database is never touched.

## Troubleshooting

* `image.repository is required`: there is no public image; build and push one.
* The release fails in `pre-install` with an admission error: the policies of the cluster apply to the hook Job too (for example a
  required CPU/memory limit: set `migration.resources`).
* The Pod restarts at once with a database error: check `database.*` and the password; `kubectl logs job/<release>-kube-res-foresight-migrate`
  (kept only while it fails) or `-c migrate` of the Pod.
* After a reinstall the stored passwords do not decrypt: the new `secretKey` differs from the old one. Reuse the old Secret.
