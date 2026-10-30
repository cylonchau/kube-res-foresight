package controller

// The permissions of the operator. They are package-level markers (not attached to a declaration) so that controller-gen turns them into
// deploy/operator/rbac/role.yaml. The operator reads the modules of the platform and writes the replicas of their targets through the
// scale subresource; nothing else is stored in the cluster.

// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments/scale;statefulsets/scale,verbs=get;update;patch
// +kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch
// +kubebuilder:rbac:groups=keda.sh,resources=scaledobjects,verbs=get;list;watch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
