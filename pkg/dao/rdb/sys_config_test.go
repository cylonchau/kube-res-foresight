package rdb

import (
	"context"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestSysConfigDAO(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.SysConfig{}, &model.SystemMarker{}))

	dao := NewSysConfigDAO(db)
	ctx := context.Background()

	// 1. Create
	config := &model.SysConfig{
		ConfigKey:   "sys.auth.token_timeout_hours",
		ConfigValue: "24",
		ConfigName:  "Token Timeout",
	}
	require.NoError(t, dao.Create(ctx, config))

	// 2. GetByKey
	fetched, err := dao.GetByKey(ctx, "sys.auth.token_timeout_hours")
	assert.NoError(t, err)
	assert.NotNil(t, fetched)
	assert.Equal(t, "24", fetched.ConfigValue)

	// 3. UpdateValue
	err = dao.UpdateValue(ctx, "sys.auth.token_timeout_hours", "12")
	assert.NoError(t, err)

	updated, err := dao.GetByKey(ctx, "sys.auth.token_timeout_hours")
	assert.NoError(t, err)
	assert.Equal(t, "12", updated.ConfigValue)
}
