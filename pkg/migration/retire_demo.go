package migration

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// The one-click demo (a synthetic workload that ran the whole pipeline) is gone: a module made of generated data looked like a real
// workload in the module list. What it made carries these names, so an upgrade can remove it and nothing else.
const (
	retiredDemoDatasource = "demo-synthetic"
	retiredDemoModule     = "demo-shop"
	retiredDemoNamespace  = "demo"
	retiredDemoWorkload   = "shop"
)

// retireDemo removes the demo module with all its stored data, the demo mappings and the demo datasource. A module is the demo only when
// its name, namespace and workload all match; a datasource only when its name matches and its type is the demo type. It returns what
// it removed, and is a no-op on a database that never had the demo.
func retireDemo(db *gorm.DB) ([]string, error) {
	ctx := context.Background()
	var removed []string

	var m model.ForecastModule
	err := db.Where("name = ? AND namespace = ? AND workload = ?", retiredDemoModule, retiredDemoNamespace, retiredDemoWorkload).Take(&m).Error
	switch {
	case err == nil:
		if m.Enabled {
			if err := db.Model(&model.ForecastModule{}).Where("id = ?", m.ID).Update("enabled", false).Error; err != nil {
				return nil, err
			}
		}
		if err := rdb.NewModuleDAO(db).Delete(ctx, m.ID); err != nil {
			return nil, err
		}
		if err := rdb.NewPredictionDAO(db).DeleteModuleData(ctx, m.ID); err != nil {
			return nil, err
		}
		removed = append(removed, fmt.Sprintf("module %s", m.Name))
	case err != gorm.ErrRecordNotFound:
		return nil, err
	}

	var ds model.Datasource
	err = db.Where("name = ? AND type = ?", retiredDemoDatasource, model.DatasourceTypeDemo).Take(&ds).Error
	switch {
	case err == nil:
		res := db.Where("datasource_id = ?", ds.ID).Delete(&model.MetricMapping{})
		if res.Error != nil {
			return nil, res.Error
		}
		if err := db.Delete(&model.Datasource{}, ds.ID).Error; err != nil {
			return nil, err
		}
		removed = append(removed, fmt.Sprintf("datasource %s with %d mappings", ds.Name, res.RowsAffected))
	case err != gorm.ErrRecordNotFound:
		return nil, err
	}
	return removed, nil
}
