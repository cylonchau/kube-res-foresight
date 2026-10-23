package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/workflow"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

const driverToken = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef"

func botAPIRecorder(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), paths...) }
}

func driverDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.SysConfig{}, &model.SystemMarker{}, &model.TelegramBot{}))
	return db
}

func TestOutboundMessagesUseTheFirstEnabledBotAndFollowChanges(t *testing.T) {
	db := driverDB(t)
	srv, paths := botAPIRecorder(t)
	ctx := context.Background()
	driver := NewDynamicChatDriver(SenderFor(db))

	// No bot and no legacy config: the message is dropped quietly.
	require.NoError(t, driver.SendMessage(ctx, 9, "hello", nil))
	assert.Empty(t, paths())

	bots := rdb.NewTelegramBotDAO(db)
	name, token, off := "alpha", driverToken, false
	bot, err := bots.Create(ctx, rdb.TelegramBotInput{Name: &name, Token: &token, APIBaseURL: &srv.URL})
	require.NoError(t, err)
	require.NoError(t, driver.SendMessage(ctx, 9, "hello", []workflow.Button{{Label: "Go", ActionType: "url", Target: "https://example.com"}}))
	assert.Equal(t, []string{"/bot" + driverToken + "/sendMessage"}, paths())

	// Disabling the bot stops outbound messages without restarting anything.
	_, err = bots.Update(ctx, bot.ID, rdb.TelegramBotInput{Enabled: &off})
	require.NoError(t, err)
	require.NoError(t, driver.SendMessage(ctx, 9, "again", nil))
	assert.Len(t, paths(), 1)

	// A recipient of zero is ignored.
	require.NoError(t, driver.SendMessage(ctx, 0, "nobody", nil))
}

func TestOutboundFallsBackToTheLegacyConfiguredBot(t *testing.T) {
	db := driverDB(t)
	srv, paths := botAPIRecorder(t)
	for key, value := range map[string]string{ConfigBotEnabled: "true", ConfigBotToken: driverToken, ConfigBotAPIBaseURL: srv.URL} {
		require.NoError(t, db.Create(&model.SysConfig{ConfigKey: key, ConfigValue: value, ConfigName: key}).Error)
	}
	require.NoError(t, NewDynamicChatDriver(SenderFor(db)).SendMessage(context.Background(), 5, "hi", nil))
	assert.Len(t, paths(), 1)

	token, _, _, ok, err := SenderFor(db)(context.Background())
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, driverToken, token)
}

func TestOutboundReportsCredentialErrors(t *testing.T) {
	boom := errors.New("credentials unavailable")
	driver := NewDynamicChatDriver(func(context.Context) (string, string, string, bool, error) { return "", "", "", false, boom })
	assert.ErrorIs(t, driver.SendMessage(context.Background(), 1, "x", nil), boom)
}
