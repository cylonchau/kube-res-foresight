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

func TestColumnMetaDAOExposesWorkflowColumns(t *testing.T) {
	columns := (&ColumnMetaDAO{}).GetModelColumnsByPage("workflow_instances")
	assert.NotEmpty(t, columns)
}
