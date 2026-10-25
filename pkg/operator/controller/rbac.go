package controller

// The operator's permissions. They are package-level markers (not attached to a declaration) so that
// controller-gen turns them into deploy/operator/rbac/role.yaml.

// +kubebuilder:rbac:groups=autoscaling.kubeforecast.io,resources=predictivescalers,verbs=get;list;watch
// +kubebuilder:rbac:groups=autoscaling.kubeforecast.io,resources=predictivescalers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments/scale;statefulsets/scale,verbs=get;update;patch
// +kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch
// +kubebuilder:rbac:groups=keda.sh,resources=scaledobjects,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
