# kube-resource-fcst-operator Helm chart

Installs the kube-resource-fcst operator: the controller that scales a Deployment or StatefulSet from the replicas the platform
predicts (`PredictiveScaler`). It replaces an HPA or KEDA for the workloads it manages; it must not share a workload with one.

**Status: written and linted, not installed in a real cluster.** `helm lint` and `helm template` pass (Helm 3.15); the
operator logic is tested against a fake Kubernetes client only.

## What it installs

| Object | Notes |
|---|---|
| CRD `predictivescalers.autoscaling.kube-resource-fcst.io` | from `crds/`: installed once, never upgraded or deleted by Helm |
| Deployment | 2 replicas, leader election on, non-root, read-only root filesystem |
| ServiceAccount, ClusterRole, ClusterRoleBinding | scale Deployments/StatefulSets, read HPA and KEDA objects (to refuse a conflict), `Secret` get, leases, events |
| Service `<release>-metrics` | port 8080, the `kube_resource_fcst_operator_*` metrics |
| ServiceMonitor | optional, needs the Prometheus Operator |
| PodDisruptionBudget | when there is more than one replica |

## Install

1. Build and push the image (there is no public one):

   ```sh
   docker build -f deploy/docker/Dockerfile --target operator -t registry.example.com/kube-resource-fcst-operator:0.1.0 .
   docker push registry.example.com/kube-resource-fcst-operator:0.1.0
   ```

2. Install the chart:

   ```sh
   helm install kube-resource-fcst-operator deploy/helm/kube-resource-fcst-operator \
     --namespace kube-resource-fcst-system --create-namespace \
     --set image.repository=registry.example.com/kube-resource-fcst-operator \
     --set image.tag=0.1.0
   ```

   Useful values: `operator.watchNamespace` (watch one namespace), `metrics.serviceMonitor.enabled`, `replicaCount`,
   `resources`, `imagePullSecrets`, `rbac.create=false` (bind a role of your own). See `values.yaml`.

3. Check it:

   ```sh
   kubectl -n kube-resource-fcst-system rollout status deploy/kube-resource-fcst-operator
   kubectl get crd predictivescalers.autoscaling.kube-resource-fcst.io
   ```

4. Create a platform credential holding only `Operator Decisions (read, report)`, put `<key>:<secret>` in a Secret and apply a
   `PredictiveScaler` in **Observe** mode: `deploy/operator/example-predictivescaler.yaml`. Move to Auto
   (`deploy/operator/example-predictivescaler-statefulset-auto.yaml`) only after its status shows the decisions you expect
   and an administrator opened `forecast.allow_auto_mode` on the platform.

## Without a PredictiveScaler: the platform module is the configuration

A `PredictiveScaler` repeats what the module already says (workload, minimum and maximum, fallback, pace), and the two can drift
apart. With `platform.url` the operator reads the platform's modules itself and scales the workload of each module that is
enabled, in **auto** mode, not in bypass, while `forecast.allow_auto_mode` is open:

```sh
kubectl -n kube-resource-fcst-system create secret generic kube-resource-fcst-platform --from-literal=credential='<access key>:<access secret>'
helm install kube-resource-fcst-operator deploy/helm/kube-resource-fcst-operator -n kube-resource-fcst-system --skip-crds   --set image.repository=<registry>/kube-resource-fcst-operator   --set platform.url=https://kube-resource-fcst.example.com --set platform.credentialSecret.name=kube-resource-fcst-platform   --set operator.predictiveScalerCRD=false
```

What is edited on the module (replica bounds, fallback replicas, per-direction step and cooldown, mode, bypass, enabled) is in
force at the next round (`platform.syncInterval`, 30 s); nothing is generated into the cluster. If the platform cannot be read
the replicas are left where they are. A `PredictiveScaler` object of the same workload, an HPA or a KEDA ScaledObject takes
precedence: the operator then does nothing for that module and says so in its report. Both ways can run side by side (leave
`predictiveScalerCRD` on and install the CRD) for different workloads. This chart has not been rendered with `helm template`
or installed in a cluster; the operator behaviour itself is covered by the unit tests of `pkg/operator/controller`.

## Upgrade

`helm upgrade` does not touch the CRD. When a release changes it, apply it first:

```sh
kubectl apply -f deploy/operator/crd/
helm upgrade kube-resource-fcst-operator deploy/helm/kube-resource-fcst-operator -n kube-resource-fcst-system --reuse-values
```

The copy in `crds/` is the same file as `deploy/operator/crd/`; regenerate both together (controller-gen) and keep them equal.

## Uninstall

```sh
kubectl delete predictivescaler --all -A     # first: the operator stops controlling the workloads
helm uninstall kube-resource-fcst-operator -n kube-resource-fcst-system
kubectl delete crd predictivescalers.autoscaling.kube-resource-fcst.io   # Helm leaves it, with every PredictiveScaler
```

## Without Helm

`deploy/operator/` has the plain manifests: `crd/`, `rbac/role.yaml`, `manager.yaml`, `monitoring/`.
