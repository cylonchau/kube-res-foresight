package rdb

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var idxSeq int32

// A list that takes a time range, or a text search that is given one by the platform, cuts the table with an index on the time: without it the range is a scan of
// the whole table, which is the query that ends in a 504 once the table has grown.
func TestEveryBigTableTheListsReadByTimeHasAnIndexOnThatTime(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:listidx%d?mode=memory&cache=shared", atomic.AddInt32(&idxSeq, 1))), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(model.Models...))
	byCreated := []any{&model.Recommendation{}, &model.ScalingExecution{}, &model.AdmissionDecision{}, &model.Prediction{}, &model.TrainingRun{}, &model.BacktestRun{},
		&model.OperationLog{}, &model.LoginAuditLog{}, &model.AssistantAudit{}, &model.AsyncTask{}, &model.Job{}}
	for _, m := range byCreated {
		assert.True(t, db.Migrator().HasIndex(m, "CreatedAt"), "%T has an index on created_at", m)
	}
	byPrediction := []any{&model.PredictionEvaluation{}, &model.TrainingRun{}}
	for _, m := range byPrediction {
		assert.True(t, db.Migrator().HasIndex(m, "idx_training_time") || db.Migrator().HasIndex(m, "idx_evaluation_time") || hasIndexOn(t, db, m, "prediction_time"),
			"%T has an index that starts with prediction_time", m)
	}
}

func hasIndexOn(t *testing.T, db *gorm.DB, m any, column string) bool {
	t.Helper()
	indexes, err := db.Migrator().GetIndexes(m)
	require.NoError(t, err)
	for _, idx := range indexes {
		cols := idx.Columns()
		if len(cols) > 0 && cols[0] == column {
			return true
		}
	}
	return false
}
