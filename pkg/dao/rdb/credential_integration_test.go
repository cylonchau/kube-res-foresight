package rdb

import (
	"context"
	"fmt"
	"github.com/cylonchau/kube-res-foresight/pkg/apis"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"testing"
)

func integrationDB(t *testing.T, models ...interface{}) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(models...))
	return db
}

func boolPtr(v bool) *bool       { return &v }
func stringPtr(v string) *string { return &v }
func intPtr(v int) *int          { return &v }

func TestSecretKeyDAODeveloperCredentialLifecycle(t *testing.T) {
	db := integrationDB(t, &model.Permission{}, &model.SecretKey{})
	ctx := context.Background()
	dao := NewSecretKeyDAO(db)
	permission := model.Permission{Name: "Read", Code: "read", Type: "BUTTON", Status: true}
	require.NoError(t, db.Create(&permission).Error)

	created, err := dao.Create(ctx, &apis.SecretKeyCreateReq{Name: "collector", ServerEnabled: true, PermissionIDs: []uint{permission.ID, permission.ID, 0}, Remark: "runtime"})
	require.NoError(t, err)
	assert.NotEmpty(t, created.AccessKey)
	assert.NotEmpty(t, created.AccessSecret)
	assert.Equal(t, []uint{permission.ID}, created.PermissionIDs)
	credential, err := dao.AuthenticateServerCredential(ctx, created.AccessKey, created.AccessSecret)
	require.NoError(t, err)
	assert.Equal(t, created.AccessKey, credential.AccessKey)
	_, err = dao.AuthenticateServerCredential(ctx, created.AccessKey, "wrong")
	assert.ErrorIs(t, err, ErrDeveloperCredentialInvalid)

	status := "true"
	rows, total, err := dao.List(ctx, &apis.SecretKeyListReq{ListOptions: apis.ListOptions{Page: 1, PageSize: 10, Keyword: "collect"}, Status: &status})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, rows, 1)
	assert.True(t, rows[0].ServerEnabled)

	newName, remark := "collector-updated", "rotated"
	updated, err := dao.Update(ctx, uint64(created.ID), &apis.SecretKeyUpdateReq{Name: &newName, Remark: &remark, RotateSecret: true})
	require.NoError(t, err)
	assert.NotEmpty(t, updated.AccessSecret)
	assert.Equal(t, newName, updated.Name)
	_, err = dao.AuthenticateServerCredential(ctx, created.AccessKey, created.AccessSecret)
	assert.Error(t, err)
	_, err = dao.AuthenticateServerCredential(ctx, updated.AccessKey, updated.AccessSecret)
	require.NoError(t, err)

	_, err = dao.Create(ctx, &apis.SecretKeyCreateReq{Name: "bad", ServerEnabled: true})
	assert.ErrorContains(t, err, "permission_ids")
	_, err = dao.Create(ctx, &apis.SecretKeyCreateReq{Name: "bad-permission", ServerEnabled: true, PermissionIDs: []uint{999}})
	assert.ErrorContains(t, err, "invalid")
	_, err = dao.Update(ctx, uint64(created.ID), &apis.SecretKeyUpdateReq{Name: stringPtr(" ")})
	assert.ErrorContains(t, err, "name")
	require.NoError(t, dao.Delete(ctx, uint64(created.ID)))
	_, err = dao.GetByID(ctx, uint64(created.ID))
	assert.Error(t, err)
}
