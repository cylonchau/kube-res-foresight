package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/clusteragent"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// The operator looks for the workloads that ask to be registered as modules of the platform: those that carry the label clusteragent.LabelRegister=true. It only reads, and only
// when the platform's switch for the cluster is on (until then it lists nothing); it reports what it found and the platform decides. It runs on the elected replica, on its
// own, so a slow or refused list never holds up what scales a workload; a role that does not let it list leaves a line in the log and nothing else.
//
// The label is selected on by the API server, so the operator reads only the workloads that carry it, in every namespace, never the others. A report is sent when what it holds
// changed, or when the switch has just been turned on: a module that somebody deleted on purpose is not made again every few minutes while the label is still on the workload
// (remove the label, or turn the switch off and on, to say it again).

// DefaultRegisterEvery is how often the workloads are looked for.
const DefaultRegisterEvery = 5 * time.Minute

// RegistrarPlatform is the platform the registrar talks to.
type RegistrarPlatform interface {
	ClusterSettings(ctx context.Context) (clusteragent.Settings, error)
	ReportRegistrations(ctx context.Context, b clusteragent.RegistrationBatch) (clusteragent.RegistrationResult, error)
}

// WorkloadRegistrar finds the workloads that ask to be registered and reports them.
type WorkloadRegistrar struct {
	// Reader reads from the API server, not from a cache: no informer over every workload of the cluster for a read every few minutes.
	Reader   client.Reader
	Platform RegistrarPlatform
	Operator string
	// Namespace restricts the operator to one namespace; empty = every namespace.
	Namespace string
	// Every is the time between two looks; Refresh, when set, gives it at each round.
	Every   time.Duration
	Refresh func() time.Duration

	mu   sync.Mutex
	last string // the hash of what was last reported with success; empty while the switch is off or nothing was reported
}

// NeedLeaderElection keeps the registrar on the elected replica only.
func (r *WorkloadRegistrar) NeedLeaderElection() bool { return true }

// Start looks at once, then every interval, until ctx ends.
func (r *WorkloadRegistrar) Start(ctx context.Context) error {
	for {
		r.Once(ctx)
		every := r.Every
		if every <= 0 {
			every = DefaultRegisterEvery
		}
		if r.Refresh != nil {
			if d := r.Refresh(); d > every {
				every = d
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(every):
		}
	}
}

// Once makes one round: the switch, the workloads that carry the label, the report.
func (r *WorkloadRegistrar) Once(ctx context.Context) {
	log := logf.Log.WithName("register")
	s, err := r.Platform.ClusterSettings(ctx)
	if err != nil {
		log.Info("the settings of the cluster could not be read from the platform: nothing is looked for this round", "error", err.Error())
		return
	}
	if !s.AutoRegister {
		r.forget()
		return
	}
	found, err := r.Candidates(ctx, s.Namespaces)
	if err != nil {
		log.Info("the workloads that ask to be registered could not be listed (the role of the operator has to let it list deployments, statefulsets and daemonsets)", "error", err.Error())
		return
	}
	hash := hashOf(found)
	r.mu.Lock()
	same := r.last == hash
	r.mu.Unlock()
	if same || len(found) == 0 {
		// nothing new: an empty list is remembered too, so the first workload that gets the label is reported at once
		r.remember(hash)
		return
	}
	res, err := r.Platform.ReportRegistrations(ctx, clusteragent.RegistrationBatch{Operator: r.Operator, Workloads: found})
	if err != nil {
		log.Info("the workloads could not be reported to the platform: they are reported again next round", "error", err.Error())
		return
	}
	if !res.Enabled {
		return // the switch was turned off between the two calls
	}
	r.remember(hash)
	if len(res.Registered) > 0 || len(res.Skipped) > 0 {
		log.Info("workloads registered as modules (switched off until their parameters are completed)", "registered", res.Registered, "existing", res.Existing, "skipped", len(res.Skipped))
	}
}

func (r *WorkloadRegistrar) remember(h string) {
	r.mu.Lock()
	r.last = h
	r.mu.Unlock()
}

func (r *WorkloadRegistrar) forget() { r.remember("") }

func hashOf(list []clusteragent.Candidate) string {
	raw, _ := json.Marshal(list)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// Candidates lists the Deployments, StatefulSets and DaemonSets that carry the label with the value "true", in the namespaces the cluster collects from, in a stable order.
func (r *WorkloadRegistrar) Candidates(ctx context.Context, scope clusteragent.Scope) ([]clusteragent.Candidate, error) {
	opts := []client.ListOption{client.MatchingLabels{clusteragent.LabelRegister: "true"}}
	if r.Namespace != "" {
		opts = append(opts, client.InNamespace(r.Namespace))
	}
	out := []clusteragent.Candidate{}
	add := func(c clusteragent.Candidate) {
		if scope.Allows(c.Namespace) {
			out = append(out, c)
		}
	}
	var deployments appsv1.DeploymentList
	if err := r.Reader.List(ctx, &deployments, opts...); err != nil && !apierrors.IsForbidden(err) {
		return nil, fmt.Errorf("deployments: %w", err)
	}
	for _, d := range deployments.Items {
		add(candidateOf("Deployment", d.Namespace, d.Name, d.Annotations, d.Spec.Replicas, d.Spec.Template.Spec.Containers))
	}
	var sets appsv1.StatefulSetList
	if err := r.Reader.List(ctx, &sets, opts...); err != nil && !apierrors.IsForbidden(err) {
		return nil, fmt.Errorf("statefulsets: %w", err)
	}
	for _, d := range sets.Items {
		add(candidateOf("StatefulSet", d.Namespace, d.Name, d.Annotations, d.Spec.Replicas, d.Spec.Template.Spec.Containers))
	}
	var daemons appsv1.DaemonSetList
	// a role that does not let the operator list one kind (the chart gives daemonsets with the inventory, the changes or this) leaves that kind out; the others are still read
	if err := r.Reader.List(ctx, &daemons, opts...); err != nil && !apierrors.IsForbidden(err) {
		return nil, fmt.Errorf("daemonsets: %w", err)
	}
	for _, d := range daemons.Items {
		add(candidateOf("DaemonSet", d.Namespace, d.Name, d.Annotations, nil, d.Spec.Template.Spec.Containers))
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		return a.Namespace+"/"+a.Kind+"/"+a.Name < b.Namespace+"/"+b.Kind+"/"+b.Name
	})
	if len(out) > clusteragent.MaxCandidates {
		out = out[:clusteragent.MaxCandidates]
	}
	return out, nil
}

// candidateOf is what the operator can read of a workload that asks to be registered: the replicas it is set to, the sum of what its containers request, the one container when
// there is only one, and the module named by the annotation copy-from. Nothing is worked out of it and nothing is guessed.
func candidateOf(kind, namespace, name string, annotations map[string]string, replicas *int32, containers []corev1.Container) clusteragent.Candidate {
	c := clusteragent.Candidate{Namespace: namespace, Kind: kind, Name: name, Replicas: replicas, CopyFrom: strings.TrimSpace(annotations[clusteragent.AnnotationCopyFrom])}
	if len(containers) == 1 {
		c.Container = containers[0].Name
	}
	var req clusteragent.Res
	for _, ct := range containers {
		req.CPUMilli += ct.Resources.Requests.Cpu().MilliValue()
		req.MemoryBytes += ct.Resources.Requests.Memory().Value()
	}
	if req.CPUMilli > 0 || req.MemoryBytes > 0 {
		c.Requests = &req
	}
	return c
}

// ReportRegistrations implements RegistrarPlatform: one report of the workloads that ask to be registered.
func (d *PlatformDecider) ReportRegistrations(ctx context.Context, b clusteragent.RegistrationBatch) (clusteragent.RegistrationResult, error) {
	payload, err := json.Marshal(b)
	if err != nil {
		return clusteragent.RegistrationResult{}, err
	}
	data, err := d.do(ctx, http.MethodPost, "/api/v1/operator/registrations", url.Values{}, strings.NewReader(string(payload)))
	if err != nil {
		return clusteragent.RegistrationResult{}, err
	}
	var env struct {
		Code int                             `json:"code"`
		Msg  string                          `json:"msg"`
		Data clusteragent.RegistrationResult `json:"data"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return clusteragent.RegistrationResult{}, fmt.Errorf("unreadable platform response: %w", err)
	}
	if env.Code != 10000 {
		return clusteragent.RegistrationResult{}, fmt.Errorf("platform error %d: %s", env.Code, env.Msg)
	}
	return env.Data, nil
}
