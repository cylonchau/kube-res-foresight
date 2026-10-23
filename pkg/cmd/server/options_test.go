package server

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/stretchr/testify/assert"
	"testing"
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
	startOperationLogRetentionLoop(ctx, configDAO, logDAO)
}
