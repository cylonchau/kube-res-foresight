package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/cylonchau/kube-res-foresight/pkg/apis"
	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/decision"
	"github.com/cylonchau/kube-res-foresight/pkg/forecast"
	"github.com/cylonchau/kube-res-foresight/pkg/forecastsvc"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/gin-gonic/gin"
)

// ForecastRouter serves modules (Kubernetes workloads) with their metric sources and fixed periods, the platform-wide
// algorithm settings and the forecasting pipeline.
type ForecastRouter struct {
	Modules     *rdb.ModuleDAO
	Configs     *rdb.AlgorithmDAO
	Mappings    *rdb.MetricMappingDAO
	Predictions *rdb.PredictionDAO
	Service     *forecastsvc.Service
	Config      *rdb.SysConfigDAO
}

// Configuration keys that open the modes writing to the cluster. They default to false: Admission and Auto
// are enabled by an administrator only after the Kyverno policy and the operator have been verified in the
// target cluster.
const (
	ConfigAllowAdmissionMode = "forecast.allow_admission_mode"
	ConfigAllowAutoMode      = "forecast.allow_auto_mode"
)

// modeAllowed reports whether a module may be put in the mode.
func (r *ForecastRouter) modeAllowed(ctx context.Context, mode string) error {
	key := ""
	switch mode {
	case "", model.ModeObserve, model.ModeRecommend:
		return nil
	case model.ModeAdmission:
		key = ConfigAllowAdmissionMode
	case model.ModeAuto:
		key = ConfigAllowAutoMode
	default:
		return fmt.Errorf("unsupported mode %q", mode)
	}
	if r.Config != nil {
		if allowed, err := r.Config.GetBool(ctx, key, false); err == nil && allowed {
			return nil
		}
	}
	return fmt.Errorf("mode %q is disabled: an administrator must set %s=true after verifying it in the target cluster", mode, key)
}

func (r *ForecastRouter) writeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, rdb.ErrNameTaken), errors.Is(err, rdb.ErrModuleEnabled), errors.Is(err, forecastsvc.ErrNotConfigured), errors.Is(err, forecastsvc.ErrPredictionOff), errors.Is(err, rdb.ErrWorkloadTaken),
		errors.Is(err, forecastsvc.ErrPreviewInvalid), errors.Is(err, forecastsvc.ErrPreviewTooLong):
		query.BadRequest(c, err)
	case errors.Is(err, forecastsvc.ErrObserveOnly), errors.Is(err, forecastsvc.ErrStale):
		query.ErrorResponse(c, http.StatusConflict, err)
	default:
		query.HandleDBError(c, err)
	}
}

func (r *ForecastRouter) module(c *gin.Context) (*model.ForecastModule, bool) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return nil, false
	}
	m, err := r.Modules.Get(c.Request.Context(), id)
	if err != nil {
		r.writeError(c, err)
		return nil, false
	}
	return m, true
}

func (r *ForecastRouter) ListModules(c *gin.Context) {
	var req apis.ModuleListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	list, total, err := r.Modules.List(c.Request.Context(), rdb.ModuleFilter{Keyword: req.Keyword, Namespace: req.Namespace, Mode: req.Mode, Enabled: boolQuery(c, "enabled")}, int(req.Page), int(req.PageSize))
	if err != nil {
		query.InternalError(c, err)
		return
	}
	summaries, err := r.Predictions.ModuleSummaries(c.Request.Context(), list)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	out := make([]moduleDTO, len(list))
	for i, m := range list {
		out[i] = moduleDTO{ForecastModule: m, Summary: summaries[m.ID]}
	}
	query.SuccessResponse(c, nil, gin.H{"list": out, "total": total, "page": req.Page, "pageSize": req.PageSize})
}

// moduleDTO is a module with what it is connected to.
type moduleDTO struct {
	*model.ForecastModule
	Summary rdb.ModuleSummary `json:"summary"`
}

func (r *ForecastRouter) GetModule(c *gin.Context) {
	if m, ok := r.module(c); ok {
		query.SuccessResponse(c, nil, m)
	}
}

func (r *ForecastRouter) CreateModule(c *gin.Context) {
	var req apis.ModuleCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := r.modeAllowed(c.Request.Context(), req.Mode); err != nil {
		query.BadRequest(c, err)
		return
	}
	m := &model.ForecastModule{
		Name: req.Name, Description: req.Description, Enabled: req.Enabled == nil || *req.Enabled, Mode: req.Mode,
		PredictEnabled: req.PredictEnabled != nil && *req.PredictEnabled, PredictMode: req.PredictMode, PredictAlgorithm: req.PredictAlgorithm,
		OOMWeightEnabled: req.OOMWeightEnabled != nil && *req.OOMWeightEnabled,
		Namespace:        strings.TrimSpace(req.Namespace), WorkloadKind: req.WorkloadKind, Workload: strings.TrimSpace(req.Workload), Service: req.Service, Container: req.Container,
		LookbackHours: req.LookbackHours, HorizonMinutes: req.HorizonMinutes, MaxFillSteps: req.MaxFillSteps,
		StepSeconds: req.StepSeconds, MinPoints: req.MinPoints, WindowSteps: req.WindowSteps, RunIntervalMinutes: req.RunIntervalMinutes,
		LimitCPURatio: req.LimitCPURatio, LimitMemoryRatio: req.LimitMemoryRatio,
		ScaleUpMaxStep: req.ScaleUpMaxStep, ScaleDownMaxStep: req.ScaleDownMaxStep,
		ScaleUpCooldownSeconds: req.ScaleUpCooldownSeconds, ScaleDownCooldownSeconds: req.ScaleDownCooldownSeconds,
		FallbackMinHistoryRatio: req.FallbackMinHistoryRatio, MaxMissingRatio: req.MaxMissingRatio,
		TTLSeconds: req.TTLSeconds, MinScore: req.MinScore,
		MinCPUMilli: req.MinCPUMilli, MaxCPUMilli: req.MaxCPUMilli, MinMemoryBytes: req.MinMemoryBytes, MaxMemoryBytes: req.MaxMemoryBytes,
		MinReplicas: req.MinReplicas, MaxReplicas: req.MaxReplicas,
		FallbackCPUMilli: req.FallbackCPUMilli, FallbackMemoryBytes: req.FallbackMemoryBytes, FallbackReplicas: req.FallbackReplicas, FixedReplicas: req.FixedReplicas,
		MinGPU: req.MinGPU, MaxGPU: req.MaxGPU, FallbackGPU: req.FallbackGPU,
		HeadroomRatio: req.HeadroomRatio, PodCapacityRPS: req.PodCapacityRPS, Timezone: req.Timezone,
		ReplicaMode: req.ReplicaMode, CapacityMode: req.CapacityMode,
		PodCPURequestMilli: req.PodCPURequestMilli, PodMemoryRequestBytes: req.PodMemoryRequestBytes, TargetUtilization: req.TargetUtilization,
		MaxReplicasMode: req.MaxReplicasMode, HardMaxReplicas: req.HardMaxReplicas, ExpandStepRatio: req.ExpandStepRatio,
		OOMLookbackDays: req.OOMLookbackDays, OOMMemoryUplift: req.OOMMemoryUplift, JVMHeapPressure: 0.85, RecoverCycles: req.RecoverCycles,
	}
	if req.JVMHeapPressure != nil {
		m.JVMHeapPressure = *req.JVMHeapPressure
	}
	if req.OOMWeight != nil {
		m.OOMWeight = *req.OOMWeight
	}
	if err := m.Validate(); err != nil {
		query.BadRequest(c, err)
		return
	}
	// the sources are checked before anything is stored
	ctx := c.Request.Context()
	if err := r.checkPredictAlgorithm(ctx, m); err != nil {
		query.BadRequest(c, err)
		return
	}
	sources := make([]*model.MetricSource, 0, len(req.Sources))
	var made []uint // mappings written for custom queries, removed again when the module is not created
	for i := range req.Sources {
		src, mapping, err := r.sourceFromReq(ctx, 0, &req.Sources[i])
		if mapping != 0 {
			made = append(made, mapping)
		}
		if err == nil {
			err = r.checkDataset(ctx, m, src)
		}
		if err != nil {
			r.dropMappings(ctx, made)
			query.BadRequest(c, fmt.Errorf("source %q: %w", req.Sources[i].Name, err))
			return
		}
		sources = append(sources, src)
	}
	if err := r.Modules.Create(ctx, m); err != nil {
		r.dropMappings(ctx, made)
		r.writeError(c, err)
		return
	}
	for _, src := range sources {
		src.ModuleID = m.ID
		if err := r.Modules.SaveSource(ctx, src); err != nil {
			_ = r.Modules.Delete(ctx, m.ID)
			r.dropMappings(ctx, made)
			r.writeError(c, err)
			return
		}
	}
	created, err := r.Modules.Get(ctx, m.ID)
	if err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, created)
}

func applyModuleUpdate(m *model.ForecastModule, req *apis.ModuleUpdateReq) {
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = strings.TrimSpace(*src)
		}
	}
	set(&m.Name, req.Name)
	set(&m.Namespace, req.Namespace)
	set(&m.WorkloadKind, req.WorkloadKind)
	set(&m.Workload, req.Workload)
	set(&m.Service, req.Service)
	set(&m.Container, req.Container)
	set(&m.Timezone, req.Timezone)
	set(&m.ReplicaMode, req.ReplicaMode)
	set(&m.CapacityMode, req.CapacityMode)
	set(&m.MaxReplicasMode, req.MaxReplicasMode)
	if req.Description != nil {
		m.Description = *req.Description
	}
	if req.Enabled != nil {
		m.Enabled = *req.Enabled
	}
	if req.Mode != nil {
		m.Mode = *req.Mode
	}
	if req.PredictEnabled != nil {
		m.PredictEnabled = *req.PredictEnabled
	}
	if req.PredictMode != nil {
		m.PredictMode = *req.PredictMode
	}
	if req.PredictAlgorithm != nil {
		m.PredictAlgorithm = strings.TrimSpace(*req.PredictAlgorithm)
	}
	if req.OOMWeightEnabled != nil {
		m.OOMWeightEnabled = *req.OOMWeightEnabled
	}
	for dst, src := range map[*int]*int{
		&m.LookbackHours: req.LookbackHours, &m.HorizonMinutes: req.HorizonMinutes, &m.MaxFillSteps: req.MaxFillSteps,
		&m.StepSeconds: req.StepSeconds, &m.MinPoints: req.MinPoints, &m.WindowSteps: req.WindowSteps, &m.RunIntervalMinutes: req.RunIntervalMinutes,
		&m.TTLSeconds: req.TTLSeconds, &m.MinReplicas: req.MinReplicas, &m.MaxReplicas: req.MaxReplicas, &m.FallbackReplicas: req.FallbackReplicas, &m.FixedReplicas: req.FixedReplicas,
		&m.HardMaxReplicas: req.HardMaxReplicas, &m.MinGPU: req.MinGPU, &m.MaxGPU: req.MaxGPU, &m.FallbackGPU: req.FallbackGPU,
		&m.OOMLookbackDays: req.OOMLookbackDays, &m.RecoverCycles: req.RecoverCycles,
		&m.ScaleUpMaxStep: req.ScaleUpMaxStep, &m.ScaleDownMaxStep: req.ScaleDownMaxStep,
		&m.ScaleUpCooldownSeconds: req.ScaleUpCooldownSeconds, &m.ScaleDownCooldownSeconds: req.ScaleDownCooldownSeconds,
	} {
		if src != nil {
			*dst = *src
		}
	}
	for dst, src := range map[*int64]*int64{
		&m.MinCPUMilli: req.MinCPUMilli, &m.MaxCPUMilli: req.MaxCPUMilli, &m.MinMemoryBytes: req.MinMemoryBytes, &m.MaxMemoryBytes: req.MaxMemoryBytes,
		&m.FallbackCPUMilli: req.FallbackCPUMilli, &m.FallbackMemoryBytes: req.FallbackMemoryBytes,
		&m.PodCPURequestMilli: req.PodCPURequestMilli, &m.PodMemoryRequestBytes: req.PodMemoryRequestBytes,
	} {
		if src != nil {
			*dst = *src
		}
	}
	for dst, src := range map[*float64]*float64{
		&m.LimitCPURatio: req.LimitCPURatio, &m.LimitMemoryRatio: req.LimitMemoryRatio, &m.HeadroomRatio: req.HeadroomRatio, &m.PodCapacityRPS: req.PodCapacityRPS, &m.TargetUtilization: req.TargetUtilization,
		&m.FallbackMinHistoryRatio: req.FallbackMinHistoryRatio, &m.MaxMissingRatio: req.MaxMissingRatio,
		&m.OOMMemoryUplift: req.OOMMemoryUplift, &m.OOMWeight: req.OOMWeight, &m.JVMHeapPressure: req.JVMHeapPressure, &m.MinScore: req.MinScore, &m.ExpandStepRatio: req.ExpandStepRatio,
	} {
		if src != nil {
			*dst = *src
		}
	}
}

func (r *ForecastRouter) UpdateModule(c *gin.Context) {
	m, ok := r.module(c)
	if !ok {
		return
	}
	var req apis.ModuleUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if req.Mode != nil {
		if err := r.modeAllowed(c.Request.Context(), *req.Mode); err != nil {
			query.BadRequest(c, err)
			return
		}
	}
	applyModuleUpdate(m, &req)
	if err := m.Validate(); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := r.checkPredictAlgorithm(c.Request.Context(), m); err != nil {
		query.BadRequest(c, err)
		return
	}
	for _, src := range m.Sources {
		if err := r.checkDataset(c.Request.Context(), m, src); err != nil {
			query.BadRequest(c, fmt.Errorf("source %q: %w", src.Name, err))
			return
		}
	}
	if err := r.Modules.Update(c.Request.Context(), m); err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, m)
}

// checkPredictAlgorithm refuses a fixed prediction mode that names an algorithm the platform does not have.
func (r *ForecastRouter) checkPredictAlgorithm(ctx context.Context, m *model.ForecastModule) error {
	if m.PredictMode != model.PredictFixed || m.PredictAlgorithm == "" {
		return nil
	}
	if err := r.Service.EnsureAlgorithms(ctx); err != nil {
		return err
	}
	if _, err := r.Configs.Get(ctx, m.PredictAlgorithm); err != nil {
		return fmt.Errorf("predict_algorithm %q is not an algorithm of the platform", m.PredictAlgorithm)
	}
	return nil
}

func (r *ForecastRouter) DeleteModule(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	if err := r.Modules.Delete(c.Request.Context(), id); err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

func (r *ForecastRouter) AlgorithmSchema(c *gin.Context) {
	if err := r.Service.EnsureAlgorithms(c.Request.Context()); err != nil {
		query.InternalError(c, err)
		return
	}
	def, ok := forecast.Lookup(c.Param("id"))
	if !ok {
		query.NotFound(c, query.ErrRecordNotFound)
		return
	}
	query.SuccessResponse(c, nil, def)
}

func (r *ForecastRouter) ListPeriods(c *gin.Context) {
	m, ok := r.module(c)
	if !ok {
		return
	}
	list, err := r.Modules.Periods(c.Request.Context(), m.ID)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, list)
}

func periodFromReq(moduleID uint, req *apis.PeriodReq) (*model.ModulePeriod, error) {
	p := &model.ModulePeriod{
		ModuleID: moduleID, Name: strings.TrimSpace(req.Name), Enabled: req.Enabled == nil || *req.Enabled, Priority: req.Priority,
		Weekdays: req.Weekdays, StartMinute: req.StartMinute, EndMinute: req.EndMinute, AbsStart: req.AbsStart, AbsEnd: req.AbsEnd,
		CPUMilli: req.CPUMilli, MemoryBytes: req.MemoryBytes, Replicas: req.Replicas, ReplicaMode: req.ReplicaMode, Remark: req.Remark,
	}
	if p.ReplicaMode == "" {
		p.ReplicaMode = decision.ReplicaWindowFixed
	}
	if p.Weekdays < 0 || p.Weekdays > 127 {
		return nil, fmt.Errorf("weekdays must be a bit mask between 0 and 127")
	}
	err := decision.ValidatePeriod(decision.FixedPeriod{
		Name: p.Name, Weekdays: uint8(p.Weekdays), StartMinute: p.StartMinute, EndMinute: p.EndMinute, AbsStart: p.AbsStart, AbsEnd: p.AbsEnd,
		CPUMilli: p.CPUMilli, MemoryBytes: p.MemoryBytes, Replicas: p.Replicas, ReplicaMode: p.ReplicaMode,
	})
	return p, err
}

func (r *ForecastRouter) CreatePeriod(c *gin.Context) {
	m, ok := r.module(c)
	if !ok {
		return
	}
	var req apis.PeriodReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	p, err := periodFromReq(m.ID, &req)
	if err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := r.Modules.SavePeriod(c.Request.Context(), p); err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, p)
}

func (r *ForecastRouter) UpdatePeriod(c *gin.Context) {
	m, ok := r.module(c)
	if !ok {
		return
	}
	pid, ok := parseUintParam(c, "pid")
	if !ok {
		return
	}
	existing, err := r.Modules.GetPeriod(c.Request.Context(), pid)
	if err != nil || existing.ModuleID != m.ID {
		query.NotFound(c, query.ErrRecordNotFound)
		return
	}
	var req apis.PeriodReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	p, err := periodFromReq(m.ID, &req)
	if err != nil {
		query.BadRequest(c, err)
		return
	}
	p.ID, p.CreatedAt = existing.ID, existing.CreatedAt
	if err := r.Modules.SavePeriod(c.Request.Context(), p); err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, p)
}

func (r *ForecastRouter) DeletePeriod(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	pid, ok := parseUintParam(c, "pid")
	if !ok {
		return
	}
	if err := r.Modules.DeletePeriod(c.Request.Context(), id, pid); err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

// Recommend decides and stores a new recommendation version for a module outside observe mode.
func (r *ForecastRouter) Recommend(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	rec, detail, err := r.Service.Recommend(c.Request.Context(), id)
	if err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"recommendation": rec, "detail": detail})
}

// ModuleNeed answers what the forecasts say a module needs (replicas, and the CPU and memory of one Pod, with the totals) next to what it is
// set to and what runs now. It reads only: nothing is stored or applied, and a module that is disabled or in observe mode is answered too, so
// the need can be read before the module is configured to act.
func (r *ForecastRouter) ModuleNeed(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	ctx, cancel := r.stepCtx(c)
	defer cancel()
	rec, detail, err := r.Service.Estimate(ctx, id)
	if err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"need": detail.Need, "algorithm": detail.Algorithm, "score": detail.Score, "degraded": detail.Degraded,
		"degrade_notes": detail.DegradeNotes, "predict": detail.Predict, "valid_until": rec.ValidUntil})
}

func recommendationFilter(c *gin.Context) rdb.RecommendationFilter {
	moduleID, _ := strconv.ParseUint(c.Query("module_id"), 10, 64)
	version, _ := strconv.Atoi(c.Query("version"))
	return rdb.RecommendationFilter{ModuleID: uint(moduleID), Mode: c.Query("mode"), Version: version, Source: c.Query("source"),
		Degraded: boolQuery(c, "degraded"), OOMFloor: boolQuery(c, "oom_floor"), LatestOnly: c.Query("latest") == "true" || c.Query("latest") == "1",
		Since: timeQuery(c, "from"), Until: timeQuery(c, "to"), Sort: c.Query("sort_by"), Dir: c.Query("sort_order")}
}

func (r *ForecastRouter) ListRecommendations(c *gin.Context) {
	page, size := pageParams(c)
	list, total, err := r.Predictions.SearchRecommendations(c.Request.Context(), recommendationFilter(c), page, size)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": list, "total": total, "page": page, "pageSize": size})
}

// Bypass makes a module keep every original value. The reason is mandatory and, like every write, is audited
// by the operation-log middleware.
func (r *ForecastRouter) Bypass(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req apis.BypassReq
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Reason) == "" {
		query.BadRequest(c, errors.New("a reason is required to bypass a module"))
		return
	}
	if err := r.Modules.SetBypass(c.Request.Context(), id, true, strings.TrimSpace(req.Reason)); err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

func (r *ForecastRouter) Resume(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	if err := r.Modules.SetBypass(c.Request.Context(), id, false, ""); err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

func jsonMarshal(v any) (string, error) {
	data, err := json.Marshal(v)
	return string(data), err
}

// Leaderboard ranks the algorithm instances bound to a module on the sources of one kind by observed accuracy.
func (r *ForecastRouter) Leaderboard(c *gin.Context) {
	m, ok := r.module(c)
	if !ok {
		return
	}
	kind := c.Query("kind")
	if _, known := model.MetricKinds[kind]; !known {
		query.BadRequest(c, fmt.Errorf("kind must be a metric kind such as cpu or memory"))
		return
	}
	entries, err := r.Service.Leaderboard(c.Request.Context(), m.ID, kind, sinceHours(c), timeQuery(c, "to"))
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, entries)
}

func marshalJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}
