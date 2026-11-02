package rdb

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

func moduleTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ForecastModule{PredictEnabled: true}, &model.MetricSource{}, &model.MetricSeries{}, &model.MetricSample{}, &model.ModulePeriod{}, &model.Recommendation{}, &model.Prediction{}, &model.PredictionEvaluation{}, &model.BacktestRun{}, &model.TrainingRun{}, &model.ScaleOutcome{},
		&model.ModuleRunState{}, &model.AlgorithmScore{}, &model.AlgorithmConfig{}))
	return db
}

func newModule(t *testing.T, dao *ModuleDAO, name string) *model.ForecastModule {
	t.Helper()
	m := &model.ForecastModule{PredictEnabled: true, Name: name, Enabled: true, Namespace: "prod", Workload: name}
	require.NoError(t, m.Validate())
	require.NoError(t, dao.Create(context.Background(), m))
	return m
}

func TestModuleSourcesGetTheirOwnKeysAndAreLoadedWithTheModule(t *testing.T) {
	ctx := context.Background()
	dao := NewModuleDAO(moduleTestDB(t))
	a, b := newModule(t, dao, "a"), newModule(t, dao, "b")
	add := func(m *model.ForecastModule, name, kind string) *model.MetricSource {
		s := &model.MetricSource{ModuleID: m.ID, Name: name, Kind: kind, Resource: model.DefaultResource(kind), MappingID: 1, Weight: 1, Stat: "p95", Enabled: true}
		require.NoError(t, s.Validate())
		require.NoError(t, dao.SaveSource(ctx, s))
		return s
	}
	first, second := add(a, "cpu", "cpu"), add(a, "cpu-b", "cpu")
	assert.Equal(t, "cpu", first.Key)
	assert.Equal(t, "cpu_2", second.Key, "the key is unique inside the module")
	assert.Equal(t, "cpu", add(b, "cpu", "cpu").Key, "another module has its own keys and names")

	dup := &model.MetricSource{ModuleID: a.ID, Name: "CPU", Kind: "cpu", Resource: "cpu", MappingID: 1, Weight: 1, Stat: "p95", Enabled: true}
	assert.ErrorIs(t, dao.SaveSource(ctx, dup), ErrNameTaken, "names are unique in a module, whatever their case")

	got, err := dao.Get(ctx, a.ID)
	require.NoError(t, err)
	assert.Len(t, got.Sources, 2)
	list, total, err := dao.List(ctx, ModuleFilter{}, 1, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	for _, m := range list {
		assert.NotEmpty(t, m.Sources)
	}

	// the key and the kind of a stored source never change
	second.Key, second.Kind = "hacked", "memory"
	require.NoError(t, dao.SaveSource(ctx, second))
	again, err := dao.GetSource(ctx, second.ID)
	require.NoError(t, err)
	assert.Equal(t, "cpu_2", again.Key)
	assert.Equal(t, "cpu", again.Kind)

	// a source of another module cannot be deleted or changed through this one
	assert.ErrorIs(t, dao.DeleteSource(ctx, b.ID, second.ID), gorm.ErrRecordNotFound)
	require.NoError(t, dao.DeleteSource(ctx, a.ID, second.ID))
}

func TestDeletingAModuleTakesItsSourcesAndHistoryWithIt(t *testing.T) {
	ctx := context.Background()
	db := moduleTestDB(t)
	dao := NewModuleDAO(db)
	m := newModule(t, dao, "gone")
	src := &model.MetricSource{ModuleID: m.ID, Name: "cpu", Kind: "cpu", Resource: "cpu", MappingID: 1, Weight: 1, Stat: "p95", Enabled: true}
	require.NoError(t, src.Validate())
	require.NoError(t, dao.SaveSource(ctx, src))
	series := &model.MetricSeries{ModuleID: m.ID, Metric: "cpu", MappingID: 1, StepSeconds: 60}
	require.NoError(t, db.Create(series).Error)
	require.NoError(t, db.Create(&model.MetricSample{SeriesID: series.ID, TS: 1, Value: 1}).Error)
	require.NoError(t, db.Create(&model.Recommendation{ModuleID: m.ID, Version: 1, Mode: "recommend"}).Error)

	assert.ErrorIs(t, dao.Delete(ctx, m.ID), ErrModuleEnabled)
	m.Enabled = false
	require.NoError(t, dao.Update(ctx, m))
	require.NoError(t, dao.Delete(ctx, m.ID))
	for _, table := range []any{&model.MetricSource{}, &model.MetricSeries{}, &model.MetricSample{}, &model.Recommendation{}} {
		var n int64
		require.NoError(t, db.Model(table).Count(&n).Error)
		assert.Zero(t, n)
	}
}

func TestAlgorithmConfigsAreEnsuredOncePerCatalogAlgorithm(t *testing.T) {
	ctx := context.Background()
	dao := NewAlgorithmDAO(moduleTestDB(t))
	ids := []string{"naive", "ewma", "lstm"}
	baseline := func(id string) bool { return id == "naive" }
	require.NoError(t, dao.Ensure(ctx, ids, baseline))
	require.NoError(t, dao.Ensure(ctx, ids, baseline), "ensuring again adds nothing")
	all, err := dao.All(ctx)
	require.NoError(t, err)
	require.Len(t, all, 3)
	enabled, err := dao.Enabled(ctx)
	require.NoError(t, err)
	require.Len(t, enabled, 1)
	assert.Equal(t, "naive", enabled[0].AlgorithmID)

	cfg, err := dao.Get(ctx, "lstm")
	require.NoError(t, err)
	cfg.Enabled = true
	require.NoError(t, dao.Save(ctx, cfg))
	require.NoError(t, dao.Ensure(ctx, ids, baseline))
	enabled, err = dao.Enabled(ctx)
	require.NoError(t, err)
	assert.Len(t, enabled, 2, "an administrator's choice survives")
	_, err = dao.Get(ctx, "prophet")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
