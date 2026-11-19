package rdb

import (
	"context"
	"testing"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTheDiagnosticsReadThePoolAndCountTheTablesOnSQLiteAndSayWhatTheyCannotRead(t *testing.T) {
	db := boundedDB(t)
	require.NoError(t, db.Create(&model.ScalingExecution{TargetName: "a"}).Error)
	require.NoError(t, db.Create(&model.ScalingExecution{TargetName: "b"}).Error)

	report, err := NewDiagnosticsDAO(db).Report(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "sqlite", report.Driver)
	assert.GreaterOrEqual(t, report.Pool.MaxOpen, 0)
	byName := map[string]TableStat{}
	for _, tb := range report.Tables {
		byName[tb.Name] = tb
	}
	assert.EqualValues(t, 2, byName["scaling_execution"].Rows)
	assert.Contains(t, byName, "forecast_prediction", "an empty table is listed too")
	assert.NotContains(t, byName, "metric_sample", "a table that does not exist is not")
	assert.NotEmpty(t, report.Unavailable, "what SQLite cannot say is said")
	assert.NotNil(t, report.Indexes)
	assert.NotNil(t, report.Running)
}
