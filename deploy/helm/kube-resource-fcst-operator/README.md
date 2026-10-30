# kube-resource-fcst-operator Helm chart

Installs the kube-resource-fcst operator: it reads the auto modules of the platform and scales their Deployments and StatefulSets
through the scale subresource. The platform module (workload, minimum and maximum, fallback, pace) is the only configuration: the
operator creates no resource of its own in the cluster, and there is no custom resource to keep in step with the module. It replaces
an HPA or KEDA for the workloads it manages; it must not share a workload with one (it then does nothing for that module and says
so in its report).

**Status: written, not installed in a real cluster.** The operator logic is tested against a fake Kubernetes client only; this
chart has not been rendered with `helm template` since the custom resource was removed.

## What it installs

| Object | Notes |
|---|---|
| Deployment | 2 replicas, leader election on, non-root, read-only root filesystem |
| ServiceAccount, ClusterRole, ClusterRoleBinding | scale Deployments/StatefulSets, read HPA and KEDA objects (to refuse a conflict), leases |
| Service `<release>-metrics` | port 8080, the `kube_resource_fcst_operator_*` metrics |
| ServiceMonitor | optional, needs the Prometheus Operator |
| PodDisruptionBudget | when there is more than one replica |

## Install

1. Build and push the image (there is no public one):

   ```sh
   docker build -f deploy/docker/Dockerfile --target operator -t registry.example.com/kube-resource-fcst-operator:0.1.0 .
   docker push registry.example.com/kube-resource-fcst-operator:0.1.0
   ```

2. Create a platform credential holding only `Operator Decisions (read, report)` and put `<access key>:<access secret>` in a Secret:

   ```sh
   kubectl create namespace kube-resource-fcst-system
   kubectl -n kube-resource-fcst-system create secret generic kube-resource-fcst-platform --from-literal=credential='<access key>:<access secret>'
   ```

3. Install the chart. `platform.url` is required:

   ```sh
   helm install kube-resource-fcst-operator deploy/helm/kube-resource-fcst-operator -n kube-resource-fcst-system \
     --set image.repository=registry.example.com/kube-resource-fcst-operator --set image.tag=0.1.0 \
     --set platform.url=https://kube-resource-fcst.example.com --set platform.credentialSecret.name=kube-resource-fcst-platform
   ```

   Useful values: `operator.watchNamespace` (watch one namespace), `metrics.serviceMonitor.enabled`, `replicaCount`,
   `resources`, `imagePullSecrets`, `rbac.create=false` (bind a role of your own). See `values.yaml`.

4. Check it: `kubectl -n kube-resource-fcst-system rollout status deploy/kube-resource-fcst-operator`.

## What is scaled

A module is scaled when it is enabled, in **auto** mode and not in bypass, while the system setting `forecast.allow_auto_mode` is open.
What is edited on the module (replica bounds, fallback replicas, per-direction step and cooldown, mode, bypass, enabled) is in force at
the next round; nothing is generated into the cluster. If the platform cannot be read, the operator goes on for a limited time with the
decision and the module list it remembers (it may raise the replicas, never lower them); when nothing usable is remembered the replicas
are held for `degrade_hold_seconds`, and only then does the module's fallback apply, in steps. After the platform is back a scale-down waits for
the stable period. What the operator did meanwhile is queued (and kept in an emptyDir spool file) and reported, once each, when the platform answers.

The time limits of the operator are system parameters of the platform (`operator.request_timeout_seconds`, `sync_interval_seconds`,
`decision_cache_ttl_seconds`, `module_cache_ttl_seconds`, `recover_answers`, `settings_refresh_seconds`, `degrade_hold_seconds`, `degrade_cooldown_seconds`, `recover_stable_seconds`). The operator asks the platform
for them and again from time to time; the flags and `platform.syncInterval` are only the values it starts with.

## Upgrade and uninstall

```sh
helm upgrade kube-resource-fcst-operator deploy/helm/kube-resource-fcst-operator -n kube-resource-fcst-system --reuse-values
helm uninstall kube-resource-fcst-operator -n kube-resource-fcst-system
```

Nothing of the operator stays in the cluster after the uninstall except the replicas it last wrote.

If an earlier version installed the `PredictiveScaler` custom resource, remove what is left of it yourself, after the new operator runs:
`kubectl delete crd predictivescalers.autoscaling.kube-resource-fcst.io` (this deletes every PredictiveScaler object, which nothing reads any more).

## Without Helm

`deploy/operator/` has the plain manifests: `rbac/role.yaml`, `manager.yaml`, `monitoring/`.
