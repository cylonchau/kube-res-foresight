package rdb

import (
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"reflect"
)

type ColumnMetaDAO struct{}

type ColumnInfo struct {
	Key          string `json:"key"`
	Title        string `json:"title"`
	Configurable bool   `json:"configurable"`
}

var PageModelMap = map[string]interface{}{
	"user_manage":            &model.User{},
	"role_manage":            &model.Role{},
	"system_config_manage":   &model.SysConfig{},
	"secret_key_manage":      &model.SecretKey{},
	"datasource_manage":      &model.Datasource{},
	"metric_mapping_manage":  &model.MetricMapping{},
	"task_center_definition": &model.TaskDefinition{},
	"task_center_audit":      &model.Job{},
	"user_column_config":     &model.UserColumnConfig{},

	// forecasting and log pages: the model's column_setting tags say which columns the user may configure
	"forecast_module_manage":   &model.ForecastModule{},
	"forecast_recommendation":  &model.Recommendation{},
	"forecast_execution":       &model.ScalingExecution{},
	"forecast_backtest":        &model.BacktestRun{},
	"forecast_evaluation_runs": &model.PredictionEvaluation{},
	"forecast_algorithm":       &model.AlgorithmConfig{},
	"forecast_algorithm_score": &model.AlgorithmScore{},
	"forecast_admission":       &model.AdmissionDecision{},
	"forecast_metric_source":   &model.MetricSource{},
	"forecast_fallback":        &model.Recommendation{},
	"system_login_log":         &model.LoginAuditLog{},
	"system_operation_log":     &model.OperationLog{},
}

func (dao *ColumnMetaDAO) GetModelColumnsByPage(pageName string) []ColumnInfo {
	m, ok := PageModelMap[pageName]
	if !ok {
		// Fallback to direct model name lookup for backward compatibility
		m = model.GetModelByName(pageName)
		if m == nil {
			return nil
		}
	}

	t := reflect.TypeOf(m)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	var columns []ColumnInfo
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		// Skip unexported fields
		if field.PkgPath != "" {
			continue
		}

		// Only explicitly annotated database fields may enter frontend column settings.
		// Associations, computed values and sensitive fields intentionally have no tag.
		columnSetting := field.Tag.Get("column_setting")
		if columnSetting == "" {
			continue
		}

		jsonTag := field.Tag.Get("json")
		if jsonTag == "" || jsonTag == "-" {
			continue
		}

		// Handle json tag options like "name,omitempty"
		cleanTag := jsonTag
		for i, char := range jsonTag {
			if char == ',' {
				cleanTag = jsonTag[:i]
				break
			}
		}

		columns = append(columns, ColumnInfo{
			Key:          cleanTag,
			Title:        cleanTag, // Frontend will map this to i18n
			Configurable: columnSetting == "true",
		})
	}
	return columns
}
