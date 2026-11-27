package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// leftAlone is the note of a Request that was not written because it would be above the Limit the container keeps.
const leftAlone = "left as it is"

// adjustPodSpec writes what the platform asked for on a Pod template: the CPU and memory Requests (and the Limits the module manages) of every container, and the Node Selector of the
// recommended node pool. It returns what it changed, in words; nothing when the template already is as asked. A Request that would be above the Limit the container keeps is left
// alone (the API server would refuse the workload), and says so.
func adjustPodSpec(spec *corev1.PodSpec, want *PodSpecChange) []string {
	var changed []string
	set := func(list corev1.ResourceList, name corev1.ResourceName, q resource.Quantity) (corev1.ResourceList, bool) {
		if list == nil {
			list = corev1.ResourceList{}
		}
		if cur, ok := list[name]; ok && cur.Cmp(q) == 0 {
			return list, false
		}
		list[name] = q
		return list, true
	}
	type wish struct {
		name    corev1.ResourceName
		request int64
		limit   int64
		milli   bool
	}
	wishes := []wish{{corev1.ResourceCPU, want.CPUMilli, want.LimitCPUMilli, true}, {corev1.ResourceMemory, want.MemoryBytes, want.LimitMemoryBytes, false}}
	for i := range spec.Containers {
		c := &spec.Containers[i]
		var did []string
		for _, w := range wishes {
			if w.request <= 0 {
				continue
			}
			quantity := func(v int64) resource.Quantity {
				if w.milli {
					return *resource.NewMilliQuantity(v, resource.DecimalSI)
				}
				return *resource.NewQuantity(v, resource.BinarySI)
			}
			request, limit := quantity(w.request), c.Resources.Limits[w.name]
			if w.limit > 0 {
				limit = quantity(w.limit)
			}
			if !limit.IsZero() && request.Cmp(limit) > 0 {
				changed = append(changed, fmt.Sprintf("%s: the %s request %s is above the limit %s, %s", c.Name, w.name, request.String(), limit.String(), leftAlone))
				continue
			}
			var did1 bool
			if c.Resources.Requests, did1 = set(c.Resources.Requests, w.name, request); did1 {
				did = append(did, fmt.Sprintf("%s request %s", w.name, request.String()))
			}
			if w.limit > 0 {
				if c.Resources.Limits, did1 = set(c.Resources.Limits, w.name, limit); did1 {
					did = append(did, fmt.Sprintf("%s limit %s", w.name, limit.String()))
				}
			}
		}
		if len(did) > 0 {
			changed = append(changed, c.Name+": "+strings.Join(did, ", "))
		}
	}
	if len(want.NodeSelector) > 0 {
		var did []string
		for _, k := range want.DropSelectorKeys {
			if _, keep := want.NodeSelector[k]; keep {
				continue
			}
			if _, ok := spec.NodeSelector[k]; ok {
				delete(spec.NodeSelector, k)
				did = append(did, "-"+k)
			}
		}
		keys := make([]string, 0, len(want.NodeSelector))
		for k := range want.NodeSelector {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if cur, ok := spec.NodeSelector[k]; ok && cur == want.NodeSelector[k] {
				continue
			}
			if spec.NodeSelector == nil {
				spec.NodeSelector = map[string]string{}
			}
			spec.NodeSelector[k] = want.NodeSelector[k]
			did = append(did, k+"="+want.NodeSelector[k])
		}
		if len(did) > 0 {
			pool := want.NodePool
			if pool == "" {
				pool = "node pool"
			}
			changed = append(changed, "Node Selector of "+pool+": "+strings.Join(did, ", "))
		}
	}
	return changed
}

// applyPodSpec patches the Pod template of the target with what the platform asked for (see adjustPodSpec) and returns what it changed. A change of the template rolls the Pods
// of the workload, which is why it runs only for a module in auto mode, only when the operator was started to adjust the Pod spec, and never from an answer it remembered.
func (a *Applier) applyPodSpec(ctx context.Context, tgt *target, want *PodSpecChange) (string, error) {
	var spec *corev1.PodSpec
	switch o := tgt.obj.(type) {
	case *appsv1.Deployment:
		spec = &o.Spec.Template.Spec
	case *appsv1.StatefulSet:
		spec = &o.Spec.Template.Spec
	default:
		return "", nil
	}
	orig, ok := tgt.obj.DeepCopyObject().(client.Object)
	if !ok {
		return "", fmt.Errorf("cannot copy the target")
	}
	changed := adjustPodSpec(spec, want)
	// a Request left alone is only said, it is no change to write
	written := 0
	for _, c := range changed {
		if !strings.HasSuffix(c, leftAlone) {
			written++
		}
	}
	if written == 0 {
		return strings.Join(changed, "; "), nil
	}
	if err := a.Patch(ctx, tgt.obj, client.MergeFrom(orig)); err != nil {
		return "", fmt.Errorf("write the Pod spec: %w", err)
	}
	return "Pod spec written (" + strings.Join(changed, "; ") + ")", nil
}
