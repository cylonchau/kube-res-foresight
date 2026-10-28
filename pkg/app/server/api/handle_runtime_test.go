package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var runtimeNow = time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)

// seedModule creates a module directly in the given mode (the API gate is tested separately).
func seedModule(t *testing.T, _ uint, mode string, mutate func(*model.ForecastModule)) *model.ForecastModule {
	t.Helper()
	// the runtime endpoints answer only while an administrator has opened the modes
	for _, key := range []string{ConfigAllowAdmissionMode, ConfigAllowAutoMode} {
		require.NoError(t, model.DB.Where("config_key = ?", key).Attrs(model.SysConfig{ConfigValue: "true", ConfigName: key}).FirstOrCreate(&model.SysConfig{ConfigKey: key}).Error)
	}
	m := &model.ForecastModule{Name: "api-" + mode, Enabled: true, Mode: mode, Namespace: "prod", Workload: "api",
		TTLSeconds: 900, HeadroomRatio: 1, Timezone: "UTC",
		MinReplicas: 2, MaxReplicas: 20, MinCPUMilli: 100, MaxCPUMilli: 4000, MinMemoryBytes: 64 << 20, MaxMemoryBytes: 4 << 30}
	if mutate != nil {
		mutate(m)
	}
	require.NoError(t, model.DB.Create(m).Error)
	return m
}

func seedRecommendation(t *testing.T, m *model.ForecastModule, mutate func(*model.Recommendation)) *model.Recommendation {
	t.Helper()
	rec := &model.Recommendation{ModuleID: m.ID, Version: 1, Mode: m.Mode, ValidUntil: runtimeNow.Add(10 * time.Minute),
		CPUSet: true, CPUMilli: 1500, CPUSource: "algorithm", MemorySet: true, MemoryBytes: 512 << 20, MemorySource: "algorithm",
		ReplicasSet: true, Replicas: 7, ReplicasSource: "algorithm", Detail: `{"replicas":{"set":true,"value":7,"source":"algorithm","reason":"peak 650 rps"}}`}
	if mutate != nil {
		mutate(rec)
	}
	require.NoError(t, model.DB.Create(rec).Error)
	return rec
}

func answer(t *testing.T, body []byte) map[string]any { return decodeData(t, body) }

func TestModesThatWriteToTheClusterStayClosedUntilAnAdministratorOpensThem(t *testing.T) {
	r, _ := forecastTestRouter(t)
	body := map[string]any{"name": "api", "namespace": "prod", "workload": "api", "mode": "admission"}
	rec := performJSON(t, r, http.MethodPost, "/modules", "", body)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "forecast.allow_admission_mode")

	require.NoError(t, model.DB.Create(&model.SysConfig{ConfigKey: ConfigAllowAdmissionMode, ConfigValue: "true", ConfigName: "x"}).Error)
	rec = performJSON(t, r, http.MethodPost, "/modules", "", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "admission", decodeData(t, rec.Body.Bytes())["mode"])

	body["name"], body["workload"], body["mode"] = "api2", "api2", "auto"
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, "/modules", "", body).Code, "auto has its own switch")
	require.NoError(t, model.DB.Create(&model.SysConfig{ConfigKey: ConfigAllowAutoMode, ConfigValue: "true", ConfigName: "y"}).Error)
	assert.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPost, "/modules", "", body).Code)
}

func TestOperatorDecisionServesOnlyAFreshAutoRecommendation(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	m := seedModule(t, cpu.ID, model.ModeAuto, nil)
	url := "/operator/decisions?module=" + m.Name

	data := answer(t, performJSON(t, r, http.MethodGet, url, "", nil).Body.Bytes())
	assert.Equal(t, false, data["available"])
	assert.Contains(t, data["reason"], "no recommendation")

	seedRecommendation(t, m, nil)
	data = answer(t, performJSON(t, r, http.MethodGet, url, "", nil).Body.Bytes())
	assert.Equal(t, true, data["available"])
	assert.EqualValues(t, 7, data["replicas"])
	assert.EqualValues(t, 1, data["version"])
	assert.Equal(t, "peak 650 rps", data["reason"])
	assert.NotEmpty(t, data["valid_until"])

	// the module is put in bypass: the operator must be told to leave the replicas alone
	require.NoError(t, model.DB.Model(m).Update("bypass", true).Error)
	data = answer(t, performJSON(t, r, http.MethodGet, url, "", nil).Body.Bytes())
	assert.Equal(t, false, data["available"])
	assert.Contains(t, data["reason"], "bypass")
	require.NoError(t, model.DB.Model(m).Update("bypass", false).Error)

	// bounds tightened after the recommendation was made
	require.NoError(t, model.DB.Model(m).Update("max_replicas", 5).Error)
	data = answer(t, performJSON(t, r, http.MethodGet, url, "", nil).Body.Bytes())
	assert.Equal(t, false, data["available"])
	assert.Contains(t, data["reason"], "bounds")
	require.NoError(t, model.DB.Model(m).Update("max_replicas", 20).Error)

	// time passes
	saved := runtimeNow
	runtimeNow = runtimeNow.Add(time.Hour)
	t.Cleanup(func() { runtimeNow = saved })
	data = answer(t, performJSON(t, r, http.MethodGet, url, "", nil).Body.Bytes())
	assert.Equal(t, false, data["available"])
	assert.Contains(t, data["reason"], "expired")

	assert.Equal(t, http.StatusNotFound, performJSON(t, r, http.MethodGet, "/operator/decisions?module=ghost", "", nil).Code)
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodGet, "/operator/decisions", "", nil).Code)
}

func TestOperatorDecisionRefusesNonAutoAndKeptOriginals(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	rec := seedModule(t, cpu.ID, model.ModeRecommend, nil)
	seedRecommendation(t, rec, nil)
	data := answer(t, performJSON(t, r, http.MethodGet, "/operator/decisions?module="+rec.Name, "", nil).Body.Bytes())
	assert.Equal(t, false, data["available"])
	assert.Contains(t, data["reason"], "not auto")

	auto := seedModule(t, cpu.ID, model.ModeAuto, func(x *model.ForecastModule) { x.Workload = "auto-api" })
	seedRecommendation(t, auto, func(x *model.Recommendation) { x.ReplicasSet, x.Replicas = false, 0 })
	data = answer(t, performJSON(t, r, http.MethodGet, "/operator/decisions?module="+auto.Name, "", nil).Body.Bytes())
	assert.Equal(t, false, data["available"])
	assert.Contains(t, data["reason"], "original")
}

func TestOperatorReportsAreValidatedAndListed(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	m := seedModule(t, cpu.ID, model.ModeAuto, nil)
	good := map[string]any{"module": m.Name, "namespace": "prod", "scaler": "api", "target_kind": "Deployment", "target_name": "api",
		"current": 4, "ready": 4, "desired": 7, "action": "ScaleUp", "result": "applied", "reason": "decision from platform", "decision_version": 3}
	require.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPost, "/operator/reports", "", good).Code)

	for name, patch := range map[string]map[string]any{
		"unknown result": {"result": "exploded"},
		"negative":       {"desired": -1},
		"unknown module": {"module": "ghost"},
		"missing target": {"target_name": ""},
	} {
		body := map[string]any{}
		for k, v := range good {
			body[k] = v
		}
		for k, v := range patch {
			body[k] = v
		}
		rec := performJSON(t, r, http.MethodPost, "/operator/reports", "", body)
		assert.GreaterOrEqual(t, rec.Code, 400, name)
	}
	rec := performJSON(t, r, http.MethodGet, fmt.Sprintf("/scaling-decisions?module_id=%d&result=applied", m.ID), "", nil)
	data := decodeData(t, rec.Body.Bytes())
	assert.EqualValues(t, 1, data["total"])
	assert.EqualValues(t, 7, data["list"].([]any)[0].(map[string]any)["desired"])
}

func TestAdmissionAnswersOnlyForFreshAdmissionModules(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	url := "/admission/recommendations?namespace=prod&workload=api&container=app"

	assert.Equal(t, false, answer(t, performJSON(t, r, http.MethodGet, url, "", nil).Body.Bytes())["apply"], "no module")
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodGet, "/admission/recommendations?namespace=prod", "", nil).Code)

	m := seedModule(t, cpu.ID, model.ModeRecommend, nil)
	data := answer(t, performJSON(t, r, http.MethodGet, url, "", nil).Body.Bytes())
	assert.Equal(t, false, data["apply"])
	assert.Contains(t, data["reason"], "not admission")

	require.NoError(t, model.DB.Model(m).Update("mode", model.ModeAdmission).Error)
	data = answer(t, performJSON(t, r, http.MethodGet, url, "", nil).Body.Bytes())
	assert.Contains(t, data["reason"], "no recommendation")

	seedRecommendation(t, m, nil)
	data = answer(t, performJSON(t, r, http.MethodGet, url, "", nil).Body.Bytes())
	assert.Equal(t, true, data["apply"])
	res := data["resources"].(map[string]any)
	assert.EqualValues(t, 1500, res["cpu_milli"])
	assert.EqualValues(t, 512<<20, res["memory_bytes"])
	assert.NotContains(t, data, "replicas", "admission never carries replicas")

	// the module is the workload, whatever container asks: every container of the pod is covered
	sidecar := answer(t, performJSON(t, r, http.MethodGet, "/admission/recommendations?namespace=prod&workload=api&container=sidecar", "", nil).Body.Bytes())
	assert.Equal(t, true, sidecar["apply"])
	// but a workload of another kind with the same name is another workload
	kind := answer(t, performJSON(t, r, http.MethodGet, "/admission/recommendations?namespace=prod&workload=api&kind=StatefulSet", "", nil).Body.Bytes())
	assert.Equal(t, false, kind["apply"], "the module manages a Deployment")
}

func TestAdmissionDropsRequestsThatBreakLimitsOrBounds(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	m := seedModule(t, cpu.ID, model.ModeAdmission, nil)
	seedRecommendation(t, m, nil)
	base := "/admission/recommendations?namespace=prod&workload=api&container=app"

	data := answer(t, performJSON(t, r, http.MethodGet, base+"&limit_cpu_milli=1000", "", nil).Body.Bytes())
	assert.Equal(t, true, data["apply"], "memory still applies")
	res := data["resources"].(map[string]any)
	assert.NotContains(t, res, "cpu_milli", "a cpu request above the cpu limit would be rejected by the API server")
	assert.EqualValues(t, 512<<20, res["memory_bytes"])
	assert.Contains(t, data["reason"], "cpu request would exceed")

	data = answer(t, performJSON(t, r, http.MethodGet, base+"&limit_cpu_milli=1000&limit_memory_bytes=1000", "", nil).Body.Bytes())
	assert.Equal(t, false, data["apply"], "nothing is left to apply")

	require.NoError(t, model.DB.Model(m).Update("max_cpu_milli", 1000).Error)
	data = answer(t, performJSON(t, r, http.MethodGet, base, "", nil).Body.Bytes())
	assert.NotContains(t, data["resources"].(map[string]any), "cpu_milli", "bounds tightened after the recommendation")
	assert.Equal(t, true, data["apply"])
}

func TestAdmissionHonoursBypassDisabledModulesAndKeptOriginals(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	m := seedModule(t, cpu.ID, model.ModeAdmission, nil)
	seedRecommendation(t, m, nil)
	url := "/admission/recommendations?namespace=prod&workload=api&container=app"

	require.NoError(t, model.DB.Model(m).Update("bypass", true).Error)
	data := answer(t, performJSON(t, r, http.MethodGet, url, "", nil).Body.Bytes())
	assert.Equal(t, false, data["apply"])
	assert.Contains(t, data["reason"], "bypass")
	var refusals int64
	require.NoError(t, model.DB.Model(&model.AdmissionDecision{}).Where("module_id = ? AND applied = ?", m.ID, false).Count(&refusals).Error)
	assert.EqualValues(t, 1, refusals, "a refusal about a module is recorded like an applied answer")
	require.NoError(t, model.DB.Model(m).Update("bypass", false).Error)

	require.NoError(t, model.DB.Model(m).Update("enabled", false).Error)
	assert.Equal(t, false, answer(t, performJSON(t, r, http.MethodGet, url, "", nil).Body.Bytes())["apply"], "disabled modules are invisible")
	require.NoError(t, model.DB.Model(m).Update("enabled", true).Error)

	keep := seedModule(t, cpu.ID, model.ModeAdmission, func(x *model.ForecastModule) { x.Name, x.Workload = "keeper", "web" })
	seedRecommendation(t, keep, func(x *model.Recommendation) { x.CPUSet, x.MemorySet = false, false })
	data = answer(t, performJSON(t, r, http.MethodGet, "/admission/recommendations?namespace=prod&workload=web&container=app", "", nil).Body.Bytes())
	assert.Equal(t, false, data["apply"])
	assert.Contains(t, data["reason"], "original")
}

func TestSwitchingAModeOffStopsTheEndpointsEvenForModulesAlreadyInIt(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	auto := seedModule(t, cpu.ID, model.ModeAuto, nil)
	seedRecommendation(t, auto, nil)
	admission := seedModule(t, cpu.ID, model.ModeAdmission, func(m *model.ForecastModule) { m.Workload = "web" })
	seedRecommendation(t, admission, nil)
	decision := "/operator/decisions?module=" + auto.Name
	admit := "/admission/recommendations?namespace=prod&workload=web&container=app"

	assert.Equal(t, true, answer(t, performJSON(t, r, http.MethodGet, decision, "", nil).Body.Bytes())["available"])
	assert.Equal(t, true, answer(t, performJSON(t, r, http.MethodGet, admit, "", nil).Body.Bytes())["apply"])

	// An administrator turns both modes off. The setting is cached for a few seconds in each process, so the
	// check uses routers with fresh caches, as a process would have after that time.
	for _, key := range []string{ConfigAllowAutoMode, ConfigAllowAdmissionMode} {
		require.NoError(t, model.DB.Model(&model.SysConfig{}).Where("config_key = ?", key).Update("config_value", "false").Error)
	}
	rt := &RuntimeRouter{Modules: rdb.NewModuleDAO(model.DB), Predictions: rdb.NewPredictionDAO(model.DB), Config: rdb.NewSysConfigDAO(model.DB), Now: func() time.Time { return runtimeNow }}
	off := gin.New()
	off.GET("/operator/decisions", rt.Decision)
	off.GET("/admission/recommendations", rt.Admission)
	data := answer(t, performJSON(t, off, http.MethodGet, decision, "", nil).Body.Bytes())
	assert.Equal(t, false, data["available"])
	assert.Contains(t, data["reason"], "switched off")
	data = answer(t, performJSON(t, off, http.MethodGet, admit, "", nil).Body.Bytes())
	assert.Equal(t, false, data["apply"])
	assert.Contains(t, data["reason"], "switched off")
	assert.Equal(t, true, data["managed"], "a module manages it even though the switch is off")
	data = answer(t, performJSON(t, off, http.MethodGet, "/admission/recommendations?namespace=prod&workload=nobody&container=app", "", nil).Body.Bytes())
	assert.Equal(t, false, data["managed"], "no module manages this workload: a policy may refuse it")
	// the refusal of a managed workload is recorded too: a platform whose Kyverno policy calls it while the switch is off still
	// shows the calls in the admission records, with the reason
	var refused []model.AdmissionDecision
	require.NoError(t, model.DB.Where("module_id = ? AND applied = ?", admission.ID, false).Find(&refused).Error)
	require.Len(t, refused, 1)
	assert.Contains(t, refused[0].Reason, "switched off")

	// without a Config at all every mode is closed (fail closed)
	closed := gin.New()
	noConfig := &RuntimeRouter{Modules: rt.Modules, Predictions: rt.Predictions, Now: rt.Now}
	closed.GET("/operator/decisions", noConfig.Decision)
	assert.Equal(t, false, answer(t, performJSON(t, closed, http.MethodGet, decision, "", nil).Body.Bytes())["available"])
}

func TestAdmissionCarriesGPUAndEveryAnswerIsRecorded(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	m := seedModule(t, cpu.ID, model.ModeAdmission, func(m *model.ForecastModule) { m.MinGPU, m.MaxGPU = 1, 4 })
	seedRecommendation(t, m, func(rec *model.Recommendation) {
		rec.GPUSet, rec.GPU, rec.GPUSource, rec.Algorithm, rec.Score = true, 2, "algorithm", "gpu_lstm", 81
	})
	base := "/admission/recommendations?namespace=prod&workload=api&container=app"

	data := answer(t, performJSON(t, r, http.MethodGet, base+"&cpu_milli=500&memory_bytes=1000", "", nil).Body.Bytes())
	assert.Equal(t, true, data["apply"])
	assert.EqualValues(t, 2, data["resources"].(map[string]any)["gpu"])
	assert.Equal(t, "gpu_lstm", data["algorithm"])

	require.NoError(t, model.DB.Model(m).Update("max_gpu", 1).Error)
	data = answer(t, performJSON(t, r, http.MethodGet, base+"&limit_cpu_milli=1000&limit_memory_bytes=1000", "", nil).Body.Bytes())
	assert.Equal(t, false, data["apply"], "a GPU count above the module's maximum is dropped")

	rec := performJSON(t, r, http.MethodGet, "/admissions?module_id="+itoa(int(m.ID))+"&sort_by=created_at&sort_order=asc", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	list := decodeData(t, rec.Body.Bytes())["list"].([]any)
	require.Len(t, list, 2, "applied and refused answers are both kept")
	first := list[0].(map[string]any)
	assert.Equal(t, true, first["applied"])
	assert.EqualValues(t, 2, first["gpu"])
	assert.EqualValues(t, 500, first["original_cpu_milli"])
	assert.Equal(t, false, list[1].(map[string]any)["applied"])
}

func TestOperatorDecisionCarriesTheBoundsAndTheDynamicCeiling(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	m := seedModule(t, cpu.ID, model.ModeAuto, func(m *model.ForecastModule) {
		m.MaxReplicasMode, m.MaxReplicas, m.HardMaxReplicas, m.ExpandStepRatio, m.MinScore = model.MaxReplicasDynamic, 10, 30, 0.5, 60
	})
	seedRecommendation(t, m, func(rec *model.Recommendation) { rec.Replicas, rec.Algorithm, rec.Score = 14, "holt", 77 })
	url := "/operator/decisions?module=" + m.Name

	data := answer(t, performJSON(t, r, http.MethodGet, url, "", nil).Body.Bytes())
	assert.Equal(t, false, data["available"], "14 is above the ceiling of 10 until the module has expanded")

	require.NoError(t, model.DB.Create(&model.ModuleRunState{ModuleID: m.ID, EffectiveMaxReplicas: 15}).Error)
	data = answer(t, performJSON(t, r, http.MethodGet, url, "", nil).Body.Bytes())
	assert.Equal(t, true, data["available"], data["reason"])
	assert.EqualValues(t, 14, data["replicas"])
	assert.EqualValues(t, 15, data["max_replicas"])
	assert.EqualValues(t, 30, data["hard_max_replicas"])
	assert.EqualValues(t, 2, data["min_replicas"])
	assert.Equal(t, "holt", data["algorithm"])

	require.NoError(t, model.DB.Model(&model.ModuleRunState{}).Where("module_id = ?", m.ID).Update("effective_max_replicas", 99).Error)
	data = answer(t, performJSON(t, r, http.MethodGet, url, "", nil).Body.Bytes())
	assert.EqualValues(t, 30, data["max_replicas"], "the expansion never passes the independent hard maximum")
}

func TestAdmissionAnswersForAutoModulesAndSetsTheLimitsAModuleManages(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	_ = cpu
	m := seedModule(t, 0, model.ModeAuto, nil)
	seedRecommendation(t, m, nil)
	base := "/admission/recommendations?namespace=prod&workload=api&container=app"

	// auto adds the replicas to what admission does, so it answers admission requests as well
	data := answer(t, performJSON(t, r, http.MethodGet, base, "", nil).Body.Bytes())
	assert.Equal(t, true, data["apply"], data["reason"])
	assert.EqualValues(t, 1500, data["resources"].(map[string]any)["cpu_milli"])
	assert.Empty(t, data["limits"], "limits stay as declared until the module manages them")

	require.NoError(t, model.DB.Model(m).Updates(map[string]any{"limit_cpu_ratio": 2, "limit_memory_ratio": 1.5}).Error)
	data = answer(t, performJSON(t, r, http.MethodGet, base, "", nil).Body.Bytes())
	limits := data["limits"].(map[string]any)
	assert.EqualValues(t, 3000, limits["cpu_milli"], "1500m x 2")
	assert.EqualValues(t, 768<<20, limits["memory_bytes"], "512Mi x 1.5")

	// a managed limit is the one that is written, so the declared limit no longer vetoes the request
	data = answer(t, performJSON(t, r, http.MethodGet, base+"&limit_cpu_milli=1000", "", nil).Body.Bytes())
	assert.EqualValues(t, 1500, data["resources"].(map[string]any)["cpu_milli"])
	assert.EqualValues(t, 3000, data["limits"].(map[string]any)["cpu_milli"])

	bad := &model.ForecastModule{Name: "x", Namespace: "n", Workload: "w", LimitCPURatio: 0.5}
	assert.ErrorContains(t, bad.Validate(), "limit_cpu_ratio")
}

func TestOperatorModulesListsTheWorkloadsTheOperatorMayScaleStraightFromTheModuleConfig(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	configs := rdb.NewSysConfigDAO(model.DB)
	r.GET("/operator/modules", (&RuntimeRouter{Modules: rdb.NewModuleDAO(model.DB), Config: configs, Now: func() time.Time { return runtimeNow }}).OperatorModules)
	auto := seedModule(t, cpu.ID, model.ModeAuto, func(m *model.ForecastModule) {
		m.Name, m.Workload, m.FallbackReplicas, m.ScaleUpMaxStep, m.ScaleDownCooldownSeconds = "web", "web-deploy", 4, 3, 120
		m.WorkloadKind = model.KindDeployment
	})
	seedModule(t, cpu.ID, model.ModeObserve, func(m *model.ForecastModule) { m.Name, m.Workload = "watched", "watched" })
	seedModule(t, cpu.ID, model.ModeAuto, func(m *model.ForecastModule) {
		m.Name, m.Workload, m.WorkloadKind = "agent", "agent", model.KindDaemonSet
	})
	paused := seedModule(t, cpu.ID, model.ModeAuto, func(m *model.ForecastModule) { m.Name, m.Workload, m.Bypass = "paused", "paused", true })

	list := func() (bool, []any) {
		data := answer(t, performJSON(t, r, http.MethodGet, "/operator/modules", "", nil).Body.Bytes())
		modules, _ := data["modules"].([]any)
		return data["auto_allowed"] == true, modules
	}
	allowed, modules := list()
	require.True(t, allowed)
	require.Len(t, modules, 1, "only an enabled auto module with replicas that is not in bypass")
	got := modules[0].(map[string]any)
	assert.Equal(t, auto.Name, got["name"])
	assert.Equal(t, "web-deploy", got["workload"])
	assert.EqualValues(t, 2, got["min_replicas"])
	assert.EqualValues(t, 20, got["max_replicas"])
	assert.EqualValues(t, 4, got["fallback_replicas"])
	assert.EqualValues(t, 3, got["scale_up_max_step"])
	assert.EqualValues(t, 120, got["scale_down_cooldown_seconds"])

	// a change on the module is what the operator reads next time: nothing else has to be updated
	require.NoError(t, model.DB.Model(auto).Updates(map[string]any{"max_replicas": 5, "min_replicas": 3}).Error)
	_, modules = list()
	assert.EqualValues(t, 5, modules[0].(map[string]any)["max_replicas"])
	assert.EqualValues(t, 3, modules[0].(map[string]any)["min_replicas"])
	require.NoError(t, model.DB.Model(paused).Update("bypass", false).Error)
	_, modules = list()
	assert.Len(t, modules, 2)

	// the administrator's switch closes the list
	require.NoError(t, configs.UpdateValue(context.Background(), ConfigAllowAutoMode, "false"))
	allowed, modules = list()
	assert.False(t, allowed)
	assert.Empty(t, modules)
}

// A record is a deployment, an update or a creation that reached the admission policy: the first Pod of a revision the platform had
// not seen. The further Pods of that revision (the operator scaling up, a replaced Pod) are answered but not kept, unless asked for.
func TestOnlyARolloutAndTheCreationOfAWorkloadAreKeptAsAdmissionRecords(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	m := seedModule(t, cpu.ID, model.ModeAdmission, nil)
	seedRecommendation(t, m, nil)
	base := "/admission/recommendations?namespace=prod&workload=api&container=app"
	ask := func(url string) {
		rec := performJSON(t, r, http.MethodGet, url, "", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
	records := func() []any {
		rec := performJSON(t, r, http.MethodGet, "/admissions?module_id="+itoa(int(m.ID))+"&sort_by=created_at&sort_order=asc", "", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return decodeData(t, rec.Body.Bytes())["list"].([]any)
	}

	ask("/admission/recommendations?namespace=prod&workload=api&container=-&kind=Deployment") // the Deployment is created
	ask(base + "&revision=abc")                                                               // first Pod of a revision: a rollout
	ask(base + "&revision=abc")                                                               // the operator scales up
	ask(base + "&revision=abc")                                                               // a Pod is replaced
	list := records()
	require.Len(t, list, 2, "the scaling Pods are not records")
	assert.Equal(t, "deploy", list[0].(map[string]any)["origin"])
	assert.Equal(t, "rollout", list[1].(map[string]any)["origin"])

	ask(base + "&revision=def") // the Deployment is updated: a new revision
	ask(base + "&revision=def")
	list = records()
	require.Len(t, list, 3)
	assert.Equal(t, "rollout", list[2].(map[string]any)["origin"])

	// asked for, the Pods of a known revision are kept too, and marked
	require.NoError(t, model.DB.Create(&model.SysConfig{ConfigKey: ConfigAdmissionRecordScaling, ConfigValue: "true", ConfigName: "x"}).Error)
	ask(base + "&revision=def")
	list = records()
	require.Len(t, list, 4)
	assert.Equal(t, "scale", list[3].(map[string]any)["origin"])

	// a caller that sends no revision keeps one record per request, as before
	ask(base)
	assert.Len(t, records(), 5)
}
