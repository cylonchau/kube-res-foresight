package rdb

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

type columnMetaFixture struct {
	Shown  string `json:"shown" column_setting:"true"`
	Hidden string `json:"hidden" column_setting:"false"`
	Legacy string `json:"legacy"`
}

func TestColumnMetaDAOHonorsColumnSettingTag(t *testing.T) {
	const page = "column_meta_fixture"
	PageModelMap[page] = &columnMetaFixture{}
	t.Cleanup(func() { delete(PageModelMap, page) })

	columns := (&ColumnMetaDAO{}).GetModelColumnsByPage(page)
	assert.Equal(t, []ColumnInfo{
		{Key: "shown", Title: "shown", Configurable: true},
		{Key: "hidden", Title: "hidden", Configurable: false},
	}, columns)
}

func TestNewPagesAreRegisteredForPerUserColumnSettings(t *testing.T) {
	for _, page := range []string{"forecast_module_manage", "forecast_recommendation", "forecast_execution", "forecast_backtest",
		"forecast_evaluation_runs", "forecast_algorithm", "forecast_metric_source", "forecast_fallback", "forecast_algorithm_score", "forecast_admission", "system_login_log", "system_operation_log"} {
		columns := (&ColumnMetaDAO{}).GetModelColumnsByPage(page)
		assert.NotEmpty(t, columns, page)
	}
	var configurable int
	for _, c := range (&ColumnMetaDAO{}).GetModelColumnsByPage("system_login_log") {
		if c.Configurable {
			configurable++
		}
	}
	assert.GreaterOrEqual(t, configurable, 5, "the login log columns can be configured per user")
}
