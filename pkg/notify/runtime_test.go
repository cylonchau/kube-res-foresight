package notify

import (
	"context"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestRunStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, Run(ctx))
}

func TestRuntimeEngineGetterSetter(t *testing.T) {
	prev := GetEngine()
	defer SetEngine(prev)
	eng := &Engine{}
	SetEngine(eng)
	require.Equal(t, eng, GetEngine())
}

func TestRunWithDatabase(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.NotificationDelivery{}))

	oldDB := model.DB
	model.DB = db
	defer func() { model.DB = oldDB }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, Run(ctx))
	require.NotNil(t, GetEngine())
}
