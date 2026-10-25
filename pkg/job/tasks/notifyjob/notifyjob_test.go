package notifyjob

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/notify"
	"github.com/cylonchau/kubeforecast/pkg/notify/template"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestEnqueueCreatesIdempotentNotificationJob(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Job{}))
	dao := rdb.NewJobDAO(db)
	enqueue := Enqueue(dao, &model.Job{ID: 42, JobType: "source"})
	msg := notify.Message{Title: "Alert", Content: "BTC", Buttons: []template.Button{{Label: "Open", ActionType: "url", ActionTarget: "https://example.com"}}}
	require.NoError(t, enqueue(context.Background(), "webhook", "100", msg))
	require.NoError(t, enqueue(context.Background(), "webhook", "100", msg))
	var jobs []model.Job
	require.NoError(t, db.Find(&jobs).Error)
	require.Len(t, jobs, 1)
	assert.Equal(t, JobType, jobs[0].JobType)
	assert.Contains(t, jobs[0].IdempotencyKey, "notify-source-42")
	var payload Payload
	require.NoError(t, json.Unmarshal([]byte(jobs[0].Payload), &payload))
	assert.Equal(t, "BTC", payload.Content)
	assert.Len(t, payload.Buttons, 1)
}
