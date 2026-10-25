package rdb

import (
	"context"
	"fmt"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// RecordKinds are the record lists whose rows can be deleted in a batch, and the table each one is. Only records of what
// happened (a score, a backtest, a measured evaluation, a training run, an execution, an admission answer) are listed: a
// recommendation snapshot and a fallback record are the audit trail and have no batch delete.
var RecordKinds = map[string]any{
	"scores":      &model.AlgorithmScore{},
	"backtests":   &model.BacktestRun{},
	"evaluations": &model.PredictionEvaluation{},
	"trainings":   &model.TrainingRun{},
	"executions":  &model.ScalingExecution{},
	"admissions":  &model.AdmissionDecision{},
}

// DeleteRecords deletes the rows of a record list by id and returns how many existed.
func (dao *PredictionDAO) DeleteRecords(ctx context.Context, kind string, ids []uint) (int64, error) {
	table, ok := RecordKinds[kind]
	if !ok {
		return 0, fmt.Errorf("records of kind %q cannot be deleted in a batch", kind)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	res := dao.db.WithContext(ctx).Where("id IN ?", ids).Delete(table)
	return res.RowsAffected, res.Error
}
