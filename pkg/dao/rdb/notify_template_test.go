package rdb

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func newNotifyTemplateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.NotifyTemplate{}))
	return db
}

func TestNotifyTemplateDAO_ListPaginatedFiltersByKeywordAndPaginates(t *testing.T) {
	db := newNotifyTemplateTestDB(t)
	ctx := context.Background()
	dao := NewNotifyTemplateDAO(db)

	require.NoError(t, db.Create(&model.NotifyTemplate{Key: "action_approved", Name: "Action approved", BodyTemplate: "x", Enabled: true}).Error)
	require.NoError(t, db.Create(&model.NotifyTemplate{Key: "action_rejected", Name: "Action rejected", BodyTemplate: "x", Enabled: true}).Error)
	require.NoError(t, db.Create(&model.NotifyTemplate{Key: "binding_welcome", Name: "Account binding welcome", BodyTemplate: "x", Enabled: true}).Error)

	byName, total, err := dao.ListPaginated(ctx, 1, 20, "Action")
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	assert.Len(t, byName, 2)

	byKey, total, err := dao.ListPaginated(ctx, 1, 20, "binding_welcome")
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, byKey, 1)
	assert.Equal(t, "binding_welcome", byKey[0].Key)

	page1, total, err := dao.ListPaginated(ctx, 1, 2, "")
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	assert.Len(t, page1, 2)

	page2, total, err := dao.ListPaginated(ctx, 2, 2, "")
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	assert.Len(t, page2, 1)
}
