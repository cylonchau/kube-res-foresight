package controller

// The permissions of the operator. They are package-level markers (not attached to a declaration) so that controller-gen turns them into
// deploy/operator/rbac/role.yaml. The operator reads the modules of the platform and writes the replicas of their targets through the
// scale subresource; nothing else is stored in the cluster. Everything else it reads (the inventory) is read-only.

// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets;daemonsets,verbs=get;list;watch
// The inventory (P10 A1) is read-only: the nodes, and the Pods of the workloads of the modules. Kubernetes cannot limit a read of Pods to the Pods of some
// workloads, only to namespaces: the operator reads the Pods that the selector of a module names, and no other, but the role allows the read.
// +kubebuilder:rbac:groups=core,resources=nodes;pods,verbs=get;list;watch
// The events of the cluster (P12 E3) are read-only too: the reasons of what happens to nodes, Pods, workloads and autoscalers.
// +kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments/scale;statefulsets/scale,verbs=get;update;patch
// +kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch
// +kubebuilder:rbac:groups=keda.sh,resources=scaledobjects,verbs=get;list;watch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
