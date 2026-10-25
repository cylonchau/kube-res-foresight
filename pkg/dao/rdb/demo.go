package rdb

import (
	"context"

	"gorm.io/gorm"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// ByName returns the datasource with the name, or nil when there is none.
func (dao *DatasourceDAO) ByName(ctx context.Context, name string) (*model.Datasource, error) {
	var ds model.Datasource
	err := dao.db.WithContext(ctx).Where("name = ?", name).First(&ds).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	return &ds, err
}

// ByName returns the mapping of a datasource with the name, or nil when there is none.
func (dao *MetricMappingDAO) ByName(ctx context.Context, datasourceID uint, name string) (*model.MetricMapping, error) {
	var m model.MetricMapping
	err := dao.db.WithContext(ctx).Where("datasource_id = ? AND name = ?", datasourceID, name).First(&m).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	return &m, err
}

// ListByDatasource returns every mapping of a datasource.
func (dao *MetricMappingDAO) ListByDatasource(ctx context.Context, datasourceID uint) ([]*model.MetricMapping, error) {
	var list []*model.MetricMapping
	err := dao.db.WithContext(ctx).Where("datasource_id = ?", datasourceID).Find(&list).Error
	return list, err
}

// DeleteModuleData removes everything stored about a module's forecasts and decisions: predictions, backtests,
// evaluations, scores, recommendations and operator reports. Deleting a module keeps these as an audit trail; this is
// for demo data, which is not worth keeping.
func (dao *PredictionDAO) DeleteModuleData(ctx context.Context, moduleID uint) error {
	return dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, table := range []interface{}{&model.Prediction{}, &model.BacktestRun{}, &model.PredictionEvaluation{}, &model.AlgorithmScore{},
			&model.Recommendation{}, &model.ScalingExecution{}, &model.AdmissionDecision{}, &model.TrainingRun{}} {
			if err := tx.Where("module_id = ?", moduleID).Delete(table).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
