package rdb

import (
	"context"
	"testing"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// A metric template is used by the metrics of the modules that chose it: the DAO says how many use each one, so a template
// still in use is not deleted from under them, and a replaced query is only removed when nobody uses the old one any more.
func TestMetricTemplatesReportTheModuleMetricsThatUseThem(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:mappingused?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.MetricMapping{}, &model.MetricSource{}))
	ctx := context.Background()
	dao := NewMetricMappingDAO(db)

	used := &model.MetricMapping{DatasourceID: 1, Name: "cpu-workload", Metric: "cpu", Level: "workload", Query: "q", Unit: "cores", StepSeconds: 60, Enabled: true}
	free := &model.MetricMapping{DatasourceID: 1, Name: "cpu-spare", Metric: "cpu", Level: "workload", Query: "q2", Unit: "cores", StepSeconds: 60, Enabled: true}
	require.NoError(t, db.Create(used).Error)
	require.NoError(t, db.Create(free).Error)
	for i, key := range []string{"cpu", "cpu2"} {
		require.NoError(t, db.Create(&model.MetricSource{ModuleID: uint(i + 1), Key: key, Name: key, Kind: "cpu", Resource: "cpu", MappingID: used.ID, Stat: "p95", Enabled: true}).Error)
	}

	counts, err := dao.UsedBy(ctx, []uint{used.ID, free.ID})
	require.NoError(t, err)
	assert.EqualValues(t, 2, counts[used.ID])
	_, present := counts[free.ID]
	assert.False(t, present, "a template nobody uses is not reported")

	removed, err := dao.DeleteIfUnused(ctx, used.ID)
	require.NoError(t, err)
	assert.False(t, removed, "a template that modules use is kept")
	removed, err = dao.DeleteIfUnused(ctx, free.ID)
	require.NoError(t, err)
	assert.True(t, removed)
}
