package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/app/query"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecastsvc"
	"github.com/cylonchau/kube-resource-fcst/pkg/logger"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/cylonchau/kube-resource-fcst/pkg/timing"
	"github.com/gin-gonic/gin"
)

// RuntimeRouter serves the two machine consumers of recommendations: kube-resource-fcst-operator (replicas) and the
// Kyverno admission policy (requests). Both are read-mostly, fail closed (they answer "do not apply" whenever
// anything is doubtful) and are reached with developer credentials limited to these routes.
type RuntimeRouter struct {
	Modules     *rdb.ModuleDAO
	Predictions *rdb.PredictionDAO
	// RunStates gives the replica ceiling in force under dynamic expansion; without it the hard ceiling applies.
	RunStates *rdb.RunStateDAO
	// Config holds the switches that open the modes writing to the cluster. Without it every mode is closed.
	Config *rdb.SysConfigDAO
	// admissionMu serialises the decision about an admission request and its record (see recordAdmission).
	admissionMu sync.Mutex
	// Fresh makes the recommendation of a module again, on the load as it is, when the stored one is older than the operator should
	// act on. Without it the operator reads what the scheduled task stored.
	Fresh interface {
		FreshRecommendation(ctx context.Context, moduleID uint, maxAge time.Duration) (*model.Recommendation, bool, error)
		DecisionMaxAge(ctx context.Context) time.Duration
		RefreshWait(ctx context.Context) time.Duration
	}
	Now func() time.Time
}

func (r *RuntimeRouter) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// storedDetail reads the explanation saved with a recommendation.
// modeOpen reports whether an administrator has opened a mode that writes to the cluster. It is checked on every
// answer, not only when a module is configured, so turning the switch off stops admission and the operator
// at once (within the short cache of the setting). A missing setting or a read error counts as closed.
func (r *RuntimeRouter) modeOpen(c *gin.Context, key string) bool {
	if r.Config == nil {
		return false
	}
	open, err := r.Config.GetBool(c.Request.Context(), key, false)
	return err == nil && open
}

func storedDetail(rec *model.Recommendation) forecastsvc.RecommendDetail {
	var d forecastsvc.RecommendDetail
	_ = json.Unmarshal([]byte(rec.Detail), &d)
	return d
}

// OperatorSettings answers GET /operator/settings with the time limits the operator runs on (request timeout, sync interval, cache
// lifetimes, how often to ask again). They are system parameters; the operator keeps the last answer and asks again from time to time,
// so a change reaches a running operator without a restart or a new flag.
// @Summary Operator settings
// @Tags Forecast
// @Router /api/v1/operator/settings [get]
func (r *RuntimeRouter) OperatorSettings(c *gin.Context) {
	settings := timing.DefaultOperatorSettings()
	if r.Config != nil {
		ctx := c.Request.Context()
		settings = timing.LoadOperator(func(key string) (string, bool) {
			v, err := r.Config.GetValueOrDefault(ctx, key, "")
			return v, err == nil && v != ""
		})
	}
	query.SuccessResponse(c, nil, settings)
}

// OperatorDecision is the answer to the operator.
type OperatorDecision struct {
	Available  bool       `json:"available"`
	Module     string     `json:"module"`
	Version    int        `json:"version,omitempty"`
	Replicas   int        `json:"replicas,omitempty"`
	ValidUntil *time.Time `json:"valid_until,omitempty"`
	// CreatedAt is when the recommendation was made, so the operator can report the age of the decision it uses.
	CreatedAt *time.Time `json:"created_at,omitempty"`
	Source    string     `json:"source,omitempty"`
	Reason    string     `json:"reason"`
	// Algorithm and Score are the algorithm instance whose forecast decided the replicas and its score.
	Algorithm string  `json:"algorithm,omitempty"`
	Score     float64 `json:"score,omitempty"`
	// Refreshed says the platform made this recommendation again for this request, because the stored one was older than the
	// operator should act on (the decision comes before the action).
	Refreshed bool `json:"refreshed,omitempty"`
	// Urgent says the replicas come from the load measured now, because the forecast was behind it: the operator raises to them
	// at once, without the cooldown or the step limit of a scale-up.
	Urgent bool `json:"urgent,omitempty"`
	// MinReplicas is the hard floor, MaxReplicas the ceiling in force now (fixed, or the dynamically expanded one) and
	// HardMaxReplicas the safety ceiling the expansion never passes. The operator refuses a decision outside them.
	MinReplicas     int `json:"min_replicas,omitempty"`
	MaxReplicas     int `json:"max_replicas,omitempty"`
	HardMaxReplicas int `json:"hard_max_replicas,omitempty"`
}

// latestRecommendation is the recommendation the operator should act on: the stored one when it is recent enough, else one the platform
// makes first (see forecastsvc.FreshRecommendation). A refresh that fails or runs out of time leaves the stored one.
func (r *RuntimeRouter) latestRecommendation(c *gin.Context, moduleID uint) (*model.Recommendation, bool, error) {
	ctx := c.Request.Context()
	if r.Fresh != nil {
		if maxAge := r.Fresh.DecisionMaxAge(ctx); maxAge > 0 {
			// how long the request waits is a system parameter, always below the time the operator waits for its own request: the
			// answer comes before the operator hangs up, and the refresh itself goes on and the next poll finds its result
			refreshCtx, cancel := context.WithTimeout(ctx, r.Fresh.RefreshWait(ctx))
			defer cancel()
			rec, refreshed, err := r.Fresh.FreshRecommendation(refreshCtx, moduleID, maxAge)
			if err == nil {
				return rec, refreshed, nil
			}
			logger.Warn("refreshing the decision for the operator failed, the stored one is used", logger.Int("module", int(moduleID)), logger.Err(err))
		}
	}
	rec, err := r.Predictions.LatestRecommendation(ctx, moduleID)
	return rec, false, err
}

// replicaCeiling is the largest replica count the module may be given now.
func (r *RuntimeRouter) replicaCeiling(c *gin.Context, m *model.ForecastModule) (effective, hard int) {
	effective, hard = m.MaxReplicas, m.MaxReplicas
	if m.MaxReplicasMode != model.MaxReplicasDynamic || m.HardMaxReplicas <= 0 {
		return effective, hard
	}
	hard = m.HardMaxReplicas
	effective = m.MaxReplicas
	if r.RunStates != nil {
		if st, err := r.RunStates.RunState(c.Request.Context(), m.ID); err == nil && st.EffectiveMaxReplicas > effective {
			effective = st.EffectiveMaxReplicas
		}
	}
	if effective > hard {
		effective = hard
	}
	return effective, hard
}

// Decision answers GET /operator/decisions?module=<name> with the replicas the operator may apply.
func (r *RuntimeRouter) Decision(c *gin.Context) {
	name := strings.TrimSpace(c.Query("module"))
	if name == "" {
		query.BadRequest(c, fmt.Errorf("module is required"))
		return
	}
	m, err := r.Modules.ByName(c.Request.Context(), name)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	out := OperatorDecision{Module: m.Name}
	deny := func(reason string) {
		out.Reason = reason
		query.SuccessResponse(c, nil, out)
	}
	switch {
	case !r.modeOpen(c, ConfigAllowAutoMode):
		deny("auto mode is switched off by an administrator (forecast.allow_auto_mode)")
		return
	case !m.Enabled:
		deny("module is disabled")
		return
	case m.Mode != model.ModeAuto:
		deny(fmt.Sprintf("module is in %s mode, not auto", m.Mode))
		return
	case m.Bypass:
		deny("module is in bypass: the operator must leave the replicas alone")
		return
	case !m.HasReplicas():
		deny("a DaemonSet has no replica count: the operator has nothing to scale")
		return
	}
	rec, refreshed, err := r.latestRecommendation(c, m.ID)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	out.Refreshed = refreshed
	switch {
	case rec == nil:
		deny("no recommendation has been made yet")
		return
	case !r.now().Before(rec.ValidUntil):
		deny("the latest recommendation has expired")
		return
	case !rec.ReplicasSet || rec.Replicas < 1:
		deny("the latest recommendation keeps the original replicas")
		return
	}
	effectiveMax, hardMax := r.replicaCeiling(c, m)
	switch {
	case effectiveMax > 0 && rec.Replicas > effectiveMax, hardMax > 0 && rec.Replicas > hardMax, rec.Replicas < m.MinReplicas:
		deny("the latest recommendation is outside the module's current replica bounds")
		return
	}
	validUntil := rec.ValidUntil.UTC()
	out.Available, out.Version, out.Replicas, out.ValidUntil, out.Source = true, rec.Version, rec.Replicas, &validUntil, rec.ReplicasSource
	createdAt := rec.CreatedAt.UTC()
	out.CreatedAt = &createdAt
	out.Reason = storedDetail(rec).Replicas.Reason
	out.Algorithm, out.Score = rec.Algorithm, rec.Score
	out.Urgent = rec.Urgent
	out.MinReplicas, out.MaxReplicas, out.HardMaxReplicas = m.MinReplicas, effectiveMax, hardMax
	query.SuccessResponse(c, nil, out)
}

// OperatorModule is one workload the operator scales, read from the module configuration: the platform is the only place
// the replicas, their bounds and the pace of the changes are configured, so nothing has to be generated into the cluster.
type OperatorModule struct {
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	WorkloadKind string `json:"workload_kind"`
	Workload     string `json:"workload"`
	// MinReplicas and MaxReplicas are the bounds in force now (a dynamic maximum is the one the run state allows);
	// HardMaxReplicas is the ceiling the maximum may never pass.
	MinReplicas     int `json:"min_replicas"`
	MaxReplicas     int `json:"max_replicas"`
	HardMaxReplicas int `json:"hard_max_replicas"`
	// FallbackReplicas is what the operator uses while there is no fresh decision (never below the current replicas); 0 = none.
	FallbackReplicas         int `json:"fallback_replicas"`
	ScaleUpMaxStep           int `json:"scale_up_max_step"`
	ScaleDownMaxStep         int `json:"scale_down_max_step"`
	ScaleUpCooldownSeconds   int `json:"scale_up_cooldown_seconds"`
	ScaleDownCooldownSeconds int `json:"scale_down_cooldown_seconds"`
}

// OperatorModules answers GET /operator/modules with the workloads the operator may scale now: enabled modules in auto
// mode that are not in bypass, while an administrator allows auto mode. An empty list is a valid answer.
func (r *RuntimeRouter) OperatorModules(c *gin.Context) {
	out := struct {
		AutoAllowed bool             `json:"auto_allowed"`
		Modules     []OperatorModule `json:"modules"`
	}{Modules: []OperatorModule{}}
	if !r.modeOpen(c, ConfigAllowAutoMode) {
		query.SuccessResponse(c, nil, out)
		return
	}
	out.AutoAllowed = true
	modules, err := r.Modules.ListEnabled(c.Request.Context())
	if err != nil {
		query.InternalError(c, err)
		return
	}
	for _, m := range modules {
		if m.Mode != model.ModeAuto || m.Bypass || !m.HasReplicas() || (m.WorkloadKind != model.KindDeployment && m.WorkloadKind != model.KindStatefulSet) {
			continue
		}
		effectiveMax, hardMax := r.replicaCeiling(c, m)
		out.Modules = append(out.Modules, OperatorModule{Name: m.Name, Namespace: m.Namespace, WorkloadKind: m.WorkloadKind, Workload: m.Workload,
			MinReplicas: m.MinReplicas, MaxReplicas: effectiveMax, HardMaxReplicas: hardMax, FallbackReplicas: m.FallbackReplicas,
			ScaleUpMaxStep: m.ScaleUpMaxStep, ScaleDownMaxStep: m.ScaleDownMaxStep,
			ScaleUpCooldownSeconds: m.ScaleUpCooldownSeconds, ScaleDownCooldownSeconds: m.ScaleDownCooldownSeconds})
	}
	query.SuccessResponse(c, nil, out)
}

// ReportReq is the operator's report after an evaluation.
type ReportReq struct {
	Module          string `json:"module" binding:"required"`
	Namespace       string `json:"namespace" binding:"required"`
	Scaler          string `json:"scaler" binding:"required"`
	TargetKind      string `json:"target_kind" binding:"required"`
	TargetName      string `json:"target_name" binding:"required"`
	Current         int    `json:"current"`
	Ready           int    `json:"ready"`
	Desired         int    `json:"desired"`
	Action          string `json:"action" binding:"required"`
	Result          string `json:"result" binding:"required"`
	Reason          string `json:"reason"`
	DecisionVersion int64  `json:"decision_version"`
	// RecordID is the operator's id of the action (unique; a repeat is acknowledged and not stored again), OccurredAt when the operator
	// took it (a report that was queued during an outage arrives late), DecisionSource, State and Operator what it followed, the state of
	// the module in an outage and the Pod.
	RecordID       string     `json:"record_id"`
	OccurredAt     *time.Time `json:"occurred_at"`
	DecisionSource string     `json:"decision_source"`
	State          string     `json:"state"`
	Operator       string     `json:"operator"`
}

// maxReportAge and maxReportSkew bound the time an operator may claim for an action: a queued report may be old (a long outage) but not
// older than the data kept, and it cannot be from the future.
const (
	maxReportAge  = 30 * 24 * time.Hour
	maxReportSkew = 5 * time.Minute
)

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Report records what the operator did.
func (r *RuntimeRouter) Report(c *gin.Context) {
	var req ReportReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	switch req.Result {
	case model.ExecApplied, model.ExecObserved, model.ExecHeld, model.ExecConflict, model.ExecError:
	default:
		query.BadRequest(c, fmt.Errorf("unknown result %q", req.Result))
		return
	}
	if req.Current < 0 || req.Ready < 0 || req.Desired < 0 {
		query.BadRequest(c, fmt.Errorf("replica counts must not be negative"))
		return
	}
	m, err := r.Modules.ByName(c.Request.Context(), req.Module)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	row := &model.ScalingExecution{
		ModuleID: m.ID, Namespace: clip(req.Namespace, 253), Scaler: clip(req.Scaler, 253), TargetKind: clip(req.TargetKind, 32),
		TargetName: clip(req.TargetName, 253), Current: req.Current, Ready: req.Ready, Desired: req.Desired,
		Action: clip(req.Action, 16), Result: req.Result, Reason: clip(req.Reason, 512), DecisionVersion: req.DecisionVersion,
		DecisionSource: clip(req.DecisionSource, 16), DegradeState: clip(req.State, 16), Operator: clip(req.Operator, 253),
	}
	now := r.now()
	row.ReportedAt, row.CreatedAt = &now, now
	if id := strings.TrimSpace(req.RecordID); id != "" {
		id = clip(id, 64)
		row.RecordID = &id
	}
	// the action is dated when the operator took it, not when the platform heard of it
	if at := req.OccurredAt; at != nil && !at.IsZero() && at.After(now.Add(-maxReportAge)) && !at.After(now.Add(maxReportSkew)) {
		row.CreatedAt = at.UTC()
	}
	if _, err := r.Predictions.SaveExecutionOnce(c.Request.Context(), row); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

// ListExecutions serves GET /scaling-decisions.
func (r *RuntimeRouter) ListExecutions(c *gin.Context) {
	page, size := pageParams(c)
	list, total, err := r.Predictions.SearchExecutions(c.Request.Context(), executionFilter(c), page, size)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": list, "total": total, "page": page, "pageSize": size})
}

// ListScaleOutcomes serves GET /scale-outcomes: the scale-ups the platform decided ahead of the load, with whether each was in time.
func (r *RuntimeRouter) ListScaleOutcomes(c *gin.Context) {
	page, size := pageParams(c)
	moduleID, _ := strconv.ParseUint(c.Query("module_id"), 10, 64)
	list, total, err := r.Predictions.ListOutcomes(c.Request.Context(), uint(moduleID), c.Query("verdict"), page, size)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": list, "total": total, "page": page, "pageSize": size})
}

// AdmissionAnswer is what the Kyverno policy reads. Apply false means "leave the Pod alone".
type AdmissionAnswer struct {
	// Managed says whether a module manages the workload at all, whatever the answer is: a policy that wants every workload
	// to have a module (and refuses the others) reads this, not the free text of Reason.
	Managed    bool       `json:"managed"`
	Apply      bool       `json:"apply"`
	Reason     string     `json:"reason"`
	Module     string     `json:"module,omitempty"`
	Version    int        `json:"version,omitempty"`
	ValidUntil *time.Time `json:"valid_until,omitempty"`
	Resources  struct {
		CPUMilli    int64 `json:"cpu_milli,omitempty"`
		MemoryBytes int64 `json:"memory_bytes,omitempty"`
		// GPU is the number of GPU cards of a new Pod; it applies to new Pods only, never to running ones.
		GPU int `json:"gpu,omitempty"`
	} `json:"resources"`
	// Limits are set only for a module that manages them (limit_cpu_ratio, limit_memory_ratio): the recommended
	// request times the ratio. A resource that is absent keeps the limit the workload declares.
	Limits struct {
		CPUMilli    int64 `json:"cpu_milli,omitempty"`
		MemoryBytes int64 `json:"memory_bytes,omitempty"`
	} `json:"limits"`
	// Algorithm and Score say which algorithm instance produced the values and how it scored.
	Algorithm string  `json:"algorithm,omitempty"`
	Score     float64 `json:"score,omitempty"`
}

// recordAdmission keeps the record of an admission, only for a request that carries the uid of a real admission request: the creation of a workload, and the first Pod of a revision the platform had not seen
// (a deployment or an update). Scaling up and a replaced Pod create more Pods of a revision that was already answered for: they are
// answered the same, but they are not admissions of a deployment, an update or a creation, so no record is kept (there is no switch for
// it). The decision about a request and its record are made under one lock, so the Pods of one rollout that arrive together are one
// rollout, not several.
func (r *RuntimeRouter) recordAdmission(c *gin.Context, m *model.ForecastModule, namespace, workload, container string, out *AdmissionAnswer, original [2]int64) {
	ctx := c.Request.Context()
	// A record stands for a real admission request: the caller (the Kyverno policy) names it by its uid. A call without one is a person
	// or a script asking what the answer would be, or the operator: it is answered like any other and leaves no record. A uid that
	// already has a record is a retry of the same request.
	uid := clip(strings.TrimSpace(c.Query("request_uid")), 64)
	if uid == "" {
		return
	}
	revision := clip(strings.TrimSpace(c.Query("revision")), 64)
	r.admissionMu.Lock()
	defer r.admissionMu.Unlock()
	if seen, err := r.Predictions.HasAdmissionRequest(ctx, uid); err != nil || seen {
		return
	}
	origin := ""
	switch {
	case container == "-":
		origin = model.AdmissionDeploy
	case revision != "":
		origin = model.AdmissionRollout
		if last, err := r.Predictions.LatestAdmission(ctx, m.ID); err == nil && last != nil && last.Revision == revision {
			return // one more Pod of a revision already answered for: scaling or a replaced Pod, not an admission to record
		}
	}
	_ = r.Predictions.SaveAdmission(ctx, &model.AdmissionDecision{ModuleID: m.ID, Namespace: clip(namespace, 253),
		Workload: clip(workload, 253), Container: clip(container, 253), Applied: out.Apply, Reason: clip(out.Reason, 512), Version: out.Version,
		CPUMilli: out.Resources.CPUMilli, MemoryBytes: out.Resources.MemoryBytes, GPU: out.Resources.GPU,
		OriginalCPUMilli: original[0], OriginalMemoryBytes: original[1], LimitCPUMilli: out.Limits.CPUMilli, LimitMemoryBytes: out.Limits.MemoryBytes,
		WorkloadKind: clip(c.Query("kind"), 16), Mode: m.Mode, Algorithm: clip(out.Algorithm, 40), Revision: revision, Origin: origin,
		RequestUID: &uid, Operation: clip(strings.ToUpper(strings.TrimSpace(c.Query("operation"))), 12)})
}

// Admission answers GET /admission/recommendations?namespace=&workload=&container= with the requests a new Pod
// of that workload may be given. It never answers with replicas; limits only for a module that manages them. The caller may pass the
// container's current limits (limit_cpu_milli, limit_memory_bytes); a request above a limit is dropped, since
// the API server would reject such a Pod.
func (r *RuntimeRouter) Admission(c *gin.Context) {
	namespace, workload, container := c.Query("namespace"), c.Query("workload"), c.Query("container")
	if namespace == "" || workload == "" {
		query.BadRequest(c, fmt.Errorf("namespace and workload are required"))
		return
	}
	var out AdmissionAnswer
	var found *model.ForecastModule
	var original [2]int64
	// answer sends the answer and keeps a record of it, so every admission can be audited afterwards
	answer := func() {
		if found != nil && r.Predictions != nil {
			r.recordAdmission(c, found, namespace, workload, container, &out, original)
		}
		query.SuccessResponse(c, nil, out)
	}
	deny := func(reason string) {
		out.Apply, out.Reason = false, reason
		answer()
	}
	// a module is a workload: namespace, kind (when the caller knows it) and name
	modules, err := r.Modules.ForWorkload(c.Request.Context(), namespace, c.Query("kind"), workload)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	var m *model.ForecastModule
	if len(modules) > 0 {
		m = modules[0]
	}
	// every answer about a module is kept, a refusal as much as an applied one: that includes the answer given while the
	// switch is off, otherwise a platform whose Kyverno policy calls it would show no admission record at all
	found = m
	out.Managed = m != nil
	original[0], _ = strconv.ParseInt(c.Query("cpu_milli"), 10, 64)
	original[1], _ = strconv.ParseInt(c.Query("memory_bytes"), 10, 64)
	if !r.modeOpen(c, ConfigAllowAdmissionMode) {
		deny("admission mode is switched off by an administrator (forecast.allow_admission_mode)")
		return
	}
	switch {
	case m == nil:
		deny("no module manages this workload")
		return
	case m.Mode != model.ModeAdmission && m.Mode != model.ModeAuto:
		// auto adds the replicas to what admission does, so it answers admission requests too
		deny(fmt.Sprintf("module %q is in %s mode, not admission", m.Name, m.Mode))
		return
	case m.Bypass:
		deny(fmt.Sprintf("module %q is in bypass", m.Name))
		return
	}
	out.Module = m.Name
	rec, err := r.Predictions.LatestRecommendation(c.Request.Context(), m.ID)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	if rec == nil {
		deny("no recommendation has been made yet")
		return
	}
	if !r.now().Before(rec.ValidUntil) {
		deny("the latest recommendation has expired")
		return
	}
	validUntil := rec.ValidUntil.UTC()
	out.Version, out.ValidUntil = rec.Version, &validUntil

	limitCPU, _ := strconv.ParseInt(c.Query("limit_cpu_milli"), 10, 64)
	limitMem, _ := strconv.ParseInt(c.Query("limit_memory_bytes"), 10, 64)
	var notes []string
	if rec.CPUSet {
		switch {
		case rec.CPUMilli < 1 || (m.MinCPUMilli > 0 && rec.CPUMilli < m.MinCPUMilli) || (m.MaxCPUMilli > 0 && rec.CPUMilli > m.MaxCPUMilli):
			notes = append(notes, "cpu is outside the module's current bounds")
		case limitCPU > 0 && m.LimitCPURatio == 0 && rec.CPUMilli > limitCPU:
			notes = append(notes, "cpu request would exceed the container's limit")
		default:
			out.Resources.CPUMilli = rec.CPUMilli
		}
	}
	if rec.MemorySet {
		switch {
		case rec.MemoryBytes < 1 || (m.MinMemoryBytes > 0 && rec.MemoryBytes < m.MinMemoryBytes) || (m.MaxMemoryBytes > 0 && rec.MemoryBytes > m.MaxMemoryBytes):
			notes = append(notes, "memory is outside the module's current bounds")
		case limitMem > 0 && m.LimitMemoryRatio == 0 && rec.MemoryBytes > limitMem:
			notes = append(notes, "memory request would exceed the container's limit")
		default:
			out.Resources.MemoryBytes = rec.MemoryBytes
		}
	}
	if rec.GPUSet {
		switch {
		case rec.GPU < 1 || (m.MinGPU > 0 && rec.GPU < m.MinGPU) || (m.MaxGPU > 0 && rec.GPU > m.MaxGPU):
			notes = append(notes, "gpu is outside the module's current bounds")
		default:
			out.Resources.GPU = rec.GPU
		}
	}
	// the limits the module manages follow the requests that are applied
	if m.LimitCPURatio > 0 && out.Resources.CPUMilli > 0 {
		out.Limits.CPUMilli = int64(math.Ceil(float64(out.Resources.CPUMilli) * m.LimitCPURatio))
	}
	if m.LimitMemoryRatio > 0 && out.Resources.MemoryBytes > 0 {
		out.Limits.MemoryBytes = int64(math.Ceil(float64(out.Resources.MemoryBytes) * m.LimitMemoryRatio))
	}
	out.Algorithm, out.Score = rec.Algorithm, rec.Score
	if out.Resources.CPUMilli == 0 && out.Resources.MemoryBytes == 0 && out.Resources.GPU == 0 {
		reason := "the recommendation keeps the original requests"
		if len(notes) > 0 {
			reason = strings.Join(notes, "; ")
		}
		deny(reason)
		return
	}
	out.Apply = true
	out.Reason = "recommendation " + strconv.Itoa(rec.Version) + " applies"
	if len(notes) > 0 {
		out.Reason += " (" + strings.Join(notes, "; ") + ")"
	}
	answer()
}

func executionFilter(c *gin.Context) rdb.ExecutionFilter {
	moduleID, _ := strconv.ParseUint(c.Query("module_id"), 10, 64)
	return rdb.ExecutionFilter{ModuleID: uint(moduleID), Result: c.Query("result"), Action: c.Query("action"), Keyword: c.Query("keyword"),
		Since: timeQuery(c, "from"), Until: timeQuery(c, "to"), Sort: c.Query("sort_by"), Dir: c.Query("sort_order")}
}
