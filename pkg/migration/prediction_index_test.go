package migration

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// On every database but PostgreSQL the step does nothing and the schema migration creates the index from the model, with the columns in the
// order the reads need: module, metric, target time, algorithm, prediction time, quantile.
func TestThePredictionTargetIndexIsCreatedFromTheModelOutsidePostgres(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, ensurePredictionTargetIndex(db), "a no-op on SQLite, with or without the table")
	require.NoError(t, db.AutoMigrate(&model.Prediction{}))
	require.NoError(t, ensurePredictionTargetIndex(db))
	assert.True(t, db.Migrator().HasIndex(&model.Prediction{}, predictionTargetIndex))
	assert.True(t, db.Migrator().HasIndex(&model.Prediction{}, "idx_prediction_lookup"), "the older indexes are kept")

	var columns []string
	require.NoError(t, db.Raw(`SELECT name FROM pragma_index_info(?) ORDER BY seqno`, predictionTargetIndex).Scan(&columns).Error)
	assert.Equal(t, []string{"module_id", "metric", "target_time", "algorithm_id", "prediction_time", "quantile_permille"}, columns)
}

func TestTheListIndexesAreInTheModelsAndTheirNamesAreTheOnesTheMigrationBuildsOnPostgreSQL(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, ensureListIndexes(db), "a no-op on SQLite")
	require.NoError(t, db.AutoMigrate(&model.Recommendation{}, &model.ScalingExecution{}, &model.PredictionEvaluation{}, &model.TrainingRun{}, &model.AdmissionDecision{}))
	models := map[string]any{"recommendation": &model.Recommendation{}, "scaling_execution": &model.ScalingExecution{}, "prediction_evaluation": &model.PredictionEvaluation{},
		"forecast_training_run": &model.TrainingRun{}, "admission_decision": &model.AdmissionDecision{}}
	for _, ix := range listIndexes {
		assert.True(t, db.Migrator().HasIndex(models[ix.table], ix.name), "%s is made by the model of %s", ix.name, ix.table)
		var cols []string
		require.NoError(t, db.Raw(`SELECT name FROM pragma_index_info(?) ORDER BY seqno`, ix.name).Scan(&cols).Error)
		assert.Equal(t, ix.columns, strings.Join(cols, ", "), "%s has the columns the PostgreSQL build uses", ix.name)
	}
}
