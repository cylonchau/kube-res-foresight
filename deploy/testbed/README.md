# Local test bed (Docker Desktop / kubeadm cluster)

Not production manifests: they exist to try the loop module -> forecast -> decision -> operator -> replicas on a local cluster.

| File | What |
|---|---|
| `load-services.yaml` | `load-api`, `load-batch`, `load-cache` (and the namespace): three more copies of `load-web`, each with its own demand knob |
| `load-web.yaml` | `load-web`: its **total** CPU demand is a knob (ConfigMap `load-web-load`) shared by the pods, so scaling out does not raise the total |
| `load.sh` | `load.sh [name] low|mid|high|spike|<millicores>` sets the demand of one load service (default `load-web`) |
| `stress.sh` | stress test of one load service, every number a parameter: `stress.sh --hold 60 --recover 60` is a four-minute **instant** test (the demand jumps to `--high` at once); `stress.sh --mode ramp --steps 4 --step-every 20 --hold 60 --recover 0` climbs in steps. `--low/--high/--hold/--recover/--steps/--step-every/--baseline/--sample/--name/--ns`, and `--dry-run` to change nothing. It ends with the replicas before, the peak, and how many seconds the first scale-up and scale-down took. `stress.sh --help` lists everything |
| `cycle.sh` | runs collect/predict/recommend (and every 5th round backtest + score) for the test modules every minute |

Needs: the platform on `127.0.0.1:20001`, Prometheus on `127.0.0.1:30090`, the operator installed (`deploy/operator`), a
developer credential with only "Operator Decisions" in the Secret `forecast-test/kube-resource-fcst-platform`, and the system
setting `forecast.allow_auto_mode` = true with the modules in `auto` mode (the decision API refuses otherwise).

The cAdvisor series of this cluster have no `container` label, so the metric mappings drop the container filter of the presets.
Capacity per pod is 100m request x 60% target = 60m, so replicas = ceil(peak CPU x headroom / 0.06).

`kubectl apply` of `load-web.yaml` resets `replicas` to 2; the operator then scales it again.

`load-web` scaler:
```yaml
apiVersion: autoscaling.kube-resource-fcst.io/v1alpha1
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
    platformAPI: {url: "http://host.docker.internal:20001", module: load-web, accessKeySecretRef: {name: kube-resource-fcst-platform, key: token}, timeoutSeconds: 5}
```

## Admission (Kyverno): requests and limits of new Pods

`deploy/kyverno/kube-resource-fcst-requests.yaml` (ClusterPolicy `kube-resource-fcst-resources`, verified on Kyverno 1.19.1) sets the requests
of new Pods and, for a module with `limit_cpu_ratio` / `limit_memory_ratio`, the limits. Modules in `admission` **or** `auto` mode
are served. A workload without a module, a platform that is down, a wrong credential and an expired recommendation all leave the
Pod as declared; none of them fails the Pod.

```sh
python deploy/testbed/seed_platform.py --platform http://127.0.0.1:20001 --mode admission --limit-cpu-ratio 2 --limit-memory-ratio 2 --credentials-dir .
kubectl -n kyverno create secret generic kube-resource-fcst-admission --from-file=credential=admission.cred
kubectl apply -f deploy/testbed/kyverno-credential.yaml
sed 's#PLATFORM_URL#http://host.docker.internal:20001#' deploy/kyverno/kube-resource-fcst-requests.yaml | kubectl apply -f -
kubectl -n forecast-test rollout restart deploy/load-api     # running Pods keep their values until the workload rolls
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

## Admission demo: no module, no deployment

`deploy/kyverno/kube-resource-fcst-require-module.yaml` refuses to create a Deployment, StatefulSet or DaemonSet in `forecast-test`
that no platform module manages; `kube-resource-fcst-requests.yaml` (above) then sets the requests/limits of the Pods of a managed
workload from its module, and the platform writes an admission decision record for every question.

```sh
# once: the Kyverno credential Secret (a developer credential with only "Admission Recommendations (read)"), the RBAC, the two policies
kubectl -n kyverno create secret generic kube-resource-fcst-admission --from-literal=credential='<access key>:<access secret>'
kubectl apply -f deploy/testbed/kyverno-credential.yaml
for f in kube-resource-fcst-requests kube-resource-fcst-require-module; do
  sed -e 's#PLATFORM_URL#http://host.docker.internal:20001#' -e 's#CREDENTIAL_SECRET#kube-resource-fcst-admission#' deploy/kyverno/$f.yaml | kubectl apply -f -
done
# the platform setting forecast.allow_admission_mode must be true

kubectl apply -f deploy/testbed/admission-demo.yaml          # refused: demo-unmanaged has no module
# create a module demo-unmanaged (forecast-test, Deployment) in the platform, apply again: admitted
kubectl -n forecast-test rollout restart deploy/load-cache    # a managed workload: its new Pods get the module's requests
```
(`kube-resource-fcst-requests.yaml` reads the Secret name from its own `urlPath`; replace it there if your Secret has another name.)
