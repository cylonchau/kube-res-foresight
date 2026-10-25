package server

import (
	"context"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/stretchr/testify/assert"
	"testing"
	"time"
)

func TestServerCommandOptions(t *testing.T) {
	opts := NewOptions()
	assert.NotNil(t, opts)
	cmd := NewCommand()
	assert.Equal(t, "server", cmd.Name())
	assert.NotNil(t, cmd.Flag("config"))
	platform := NewPlatformCommand()
	assert.Equal(t, "platform", platform.Name())
	opts.ConfigFile = ""
	assert.NoError(t, opts.Complete())
	PrintFlags(cmd.Flags())
	db := setupJWTSecretDB(t)
	assert.NoError(t, db.AutoMigrate(&model.OperationLog{}))
	configDAO, logDAO := rdb.NewSysConfigDAO(db), rdb.NewOperationLogDAO(db)
	purgeOperationLogsOnce(context.Background(), configDAO, logDAO)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	startOperationLogRetentionLoop(ctx, configDAO, logDAO, rdb.NewLoginAuditLogDAO(db))
}

func TestPurgeLoginLogsHonoursRetentionSetting(t *testing.T) {
	db := setupJWTSecretDB(t)
	assert.NoError(t, db.AutoMigrate(&model.LoginAuditLog{}))
	configDAO, dao := rdb.NewSysConfigDAO(db), rdb.NewLoginAuditLogDAO(db)
	old := model.LoginAuditLog{Username: "old", Success: true, CreatedAt: time.Now().AddDate(0, 0, -10)}
	fresh := model.LoginAuditLog{Username: "fresh", Success: true, CreatedAt: time.Now()}
	assert.NoError(t, db.Create(&old).Error)
	assert.NoError(t, db.Create(&fresh).Error)
	assert.NoError(t, db.Create(&model.SysConfig{ConfigKey: loginLogRetentionDaysKey, ConfigValue: "7"}).Error)

	purgeLoginLogsOnce(context.Background(), configDAO, dao)

	var names []string
	assert.NoError(t, db.Model(&model.LoginAuditLog{}).Pluck("username", &names).Error)
	assert.Equal(t, []string{"fresh"}, names)
}
