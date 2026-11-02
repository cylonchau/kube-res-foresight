package job

import (
	"context"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
	"time"
)

func TestJobCommandOptionsAndMigration(t *testing.T) {
	opts := NewOptions()
	assert.Equal(t, 10, opts.MaxWorkers)
	assert.Equal(t, 10*time.Millisecond, opts.PollInterval)
	cmd := NewCommand()
	assert.Equal(t, "job", cmd.Name())
	assert.NotNil(t, cmd.Flag("poll-interval"))
	opts.ConfigFile = ""
	assert.NoError(t, opts.Complete())
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	runtime := NewRuntime(db, 1, time.Millisecond)
	assert.NotNil(t, runtime.scheduler)
	assert.NotNil(t, runtime.taskDAO)
	assert.NotNil(t, runtime.jobDAO)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.NoError(t, runtime.Run(ctx))
	require.NoError(t, db.AutoMigrate(&model.TaskDefinition{}, &model.Job{}))
	done := make(chan struct{})
	go func() { runTaskScanner(ctx, runtime.taskDAO, runtime.jobDAO); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scanner did not stop")
	}
}
