package clustersvc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-res-foresight/pkg/clusteragent"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// withDeletable adds the tables the deletion of a module cleans.
func (f *fixture) withDeletable(t *testing.T) {
	t.Helper()
	require.NoError(t, f.db.AutoMigrate(&model.MetricSeries{}, &model.MetricSample{}, &model.ModuleTrigger{}, &model.ModuleRunState{}, &model.AlgorithmScore{}, &model.Recommendation{},
		&model.Prediction{}, &model.PredictionEvaluation{}, &model.BacktestRun{}, &model.TrainingRun{}, &model.ScaleOutcome{}, &model.RuleDecision{}))
}

// A module that registered itself and was deleted by a person is not made again by the next report; a person lets it be registered again; a module somebody made by hand
// leaves no tombstone.
func TestADeletedSelfRegisteredModuleStaysDeletedUntilAPersonAllowsItAgain(t *testing.T) {
	f := newFixture(t)
	f.withModules(t)
	f.withDeletable(t)
	require.NoError(t, f.db.AutoMigrate(&model.RegistrationTombstone{}))
	f.def.AutoRegister = true
	ctx := context.Background()
	batch := clusteragent.RegistrationBatch{Workloads: []clusteragent.Candidate{web("shop", "api"), web("shop", "db")}}

	res, err := f.svc.Register(ctx, f.def, batch)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"shop-api", "shop-db"}, res.Registered)

	// a module made by hand (not registered by itself) is deleted: nothing is remembered
	hand := &model.ForecastModule{Name: "by-hand", Namespace: "shop", WorkloadKind: "Deployment", Workload: "hand"}
	require.NoError(t, f.svc.Modules.Create(ctx, hand))
	require.NoError(t, f.svc.Modules.Delete(ctx, hand.ID))
	list, err := f.svc.Modules.Tombstones(ctx)
	require.NoError(t, err)
	assert.Empty(t, list, "a module made by hand leaves no tombstone")

	// the person deletes the self-registered module of shop/api
	api, err := f.svc.Modules.ByName(ctx, "shop-api")
	require.NoError(t, err)
	require.NoError(t, f.svc.Modules.Delete(ctx, api.ID))
	list, err = f.svc.Modules.Tombstones(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "shop-api", list[0].ModuleName)
	assert.Equal(t, "api", list[0].Workload)

	// the operator reports the same workloads again (the label is still on them): the deleted one is not made again, the other is left as it is
	res, err = f.svc.Register(ctx, f.def, batch)
	require.NoError(t, err)
	assert.Empty(t, res.Registered, "shop/api stays deleted")
	assert.Equal(t, 1, res.Declined)
	assert.Equal(t, 1, res.Existing, "shop/db is a module already")
	gone, err := f.svc.Modules.ByName(ctx, "shop-api")
	assert.Error(t, err)
	assert.Nil(t, gone)

	// a person allows it: the next report makes it again
	require.NoError(t, f.svc.Modules.DeleteTombstone(ctx, list[0].ID))
	res, err = f.svc.Register(ctx, f.def, batch)
	require.NoError(t, err)
	assert.Equal(t, []string{"shop-api"}, res.Registered)
	assert.Zero(t, res.Declined)
	assert.Error(t, f.svc.Modules.DeleteTombstone(ctx, list[0].ID), "it is gone: a second delete finds nothing")
}

// A database that has not got the table yet (it was not migrated) still deletes and registers as it did.
func TestWithoutTheTombstoneTableADeletionAndARegistrationWorkAsBefore(t *testing.T) {
	f := newFixture(t)
	f.withModules(t)
	f.withDeletable(t)
	f.def.AutoRegister = true
	ctx := context.Background()
	res, err := f.svc.Register(ctx, f.def, clusteragent.RegistrationBatch{Workloads: []clusteragent.Candidate{web("shop", "api")}})
	require.NoError(t, err)
	require.Equal(t, []string{"shop-api"}, res.Registered)
	m, err := f.svc.Modules.ByName(ctx, "shop-api")
	require.NoError(t, err)
	require.NoError(t, f.svc.Modules.Delete(ctx, m.ID))
	list, err := f.svc.Modules.Tombstones(ctx)
	require.NoError(t, err)
	assert.Empty(t, list)
}
