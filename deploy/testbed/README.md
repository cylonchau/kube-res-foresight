# Local test bed (Docker Desktop / kubeadm cluster)

Not production manifests: they exist to try the loop module -> forecast -> decision -> operator -> replicas on a local cluster.

| File | What |
|---|---|
| `workloads.yaml` | `web-diurnal` (30 min wave), `batch-spike` (burst every 10 min), `steady-api` (constant): CPU curves for forecasting |
| `load-web.yaml` | `load-web`: its **total** CPU demand is a knob (ConfigMap `load-web-load`) shared by the pods, so scaling out does not raise the total |
| `predictivescalers.yaml` | Observe-mode scalers for the first three; `load-web` is applied separately in Auto mode (see below) |
| `load.sh` | `load.sh low|mid|high|spike|<millicores>` sets the demand of `load-web` |
| `cycle.sh` | runs collect/predict/recommend (and every 5th round backtest + score) for the test modules every minute |

Needs: the platform on `127.0.0.1:20001`, Prometheus on `127.0.0.1:30090`, the operator installed (`deploy/operator`), a
developer credential with only "Operator Decisions" in the Secret `forecast-test/kubeforecast-platform`, and the system
setting `forecast.allow_auto_mode` = true with the modules in `auto` mode (the decision API refuses otherwise).

The cAdvisor series of this cluster have no `container` label, so the metric mappings drop the container filter of the presets.
Capacity per pod is 100m request x 60% target = 60m, so replicas = ceil(peak CPU x headroom / 0.06).

`kubectl apply` of `load-web.yaml` resets `replicas` to 2; the operator then scales it again.

`load-web` scaler:
```yaml
apiVersion: autoscaling.kubeforecast.io/v1alpha1
kind: PredictiveScaler
metadata: {name: load-web, namespace: forecast-test}
spec:
  scaleTargetRef: {kind: Deployment, name: load-web}
  mode: Auto
  minReplicas: 1
  maxReplicas: 10
  fallbackReplicas: 2
  evaluationInterval: 30s
  behavior: {scaleUp: {maxStep: 3, cooldown: 1m}, scaleDown: {maxStep: 1, cooldown: 2m}}
  decision:
    source: PlatformAPI
    platformAPI: {url: "http://host.docker.internal:20001", module: load-web, accessKeySecretRef: {name: kubeforecast-platform, key: token}, timeoutSeconds: 5}
```

## Admission (Kyverno): requests and limits of new Pods

`deploy/kyverno/kubeforecast-requests.yaml` (ClusterPolicy `kubeforecast-resources`, verified on Kyverno 1.19.1) sets the requests
of new Pods and, for a module with `limit_cpu_ratio` / `limit_memory_ratio`, the limits. Modules in `admission` **or** `auto` mode
are served. A workload without a module, a platform that is down, a wrong credential and an expired recommendation all leave the
Pod as declared; none of them fails the Pod.

```sh
python deploy/testbed/seed_platform.py --platform http://127.0.0.1:20001 --mode admission --limit-cpu-ratio 2 --limit-memory-ratio 2 --credentials-dir .
kubectl -n kyverno create secret generic kubeforecast-admission --from-file=credential=admission.cred
kubectl apply -f deploy/testbed/kyverno-credential.yaml
sed 's#PLATFORM_URL#http://host.docker.internal:20001#' deploy/kyverno/kubeforecast-requests.yaml | kubectl apply -f -
kubectl -n forecast-test rollout restart deploy/steady-api     # running Pods keep their values until the workload rolls
```

Replicas are not touched by Kyverno: the operator owns them (a module in `auto` mode gets both).

## Platform-driven operator: no PredictiveScaler object

With `--platform-url` the operator reads the auto modules from the platform and scales their workloads; the module (bounds, fallback,
step and cooldown per direction, mode, bypass) is the only configuration. Verified here with a scratch platform and a local operator
against this cluster (`load-mod`, a copy of `load-web`; no PredictiveScaler of it existed): the decision took 2 -> 4 replicas, and
setting `max_replicas` to 3 on the module took it 4 -> 3 on the next round, with the 1-replica scale-down step.

```sh
go build -o operator.exe ./cmd/operator
./operator.exe --leader-elect=false --predictivescaler-crd=false --platform-url=http://127.0.0.1:20001 \
  --platform-credential-file=operator.cred --platform-sync-interval=10s
python deploy/testbed/seed_platform.py --modules load-mod --mode auto --scale-up-max-step 3 --scale-down-max-step 1 --scale-down-cooldown 60
```
Keep `--predictivescaler-crd=false` when another operator already runs in the cluster, or both would reconcile the same objects.
