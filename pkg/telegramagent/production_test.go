package telegramagent

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// botAPI answers getMe the way Telegram does, or rejects the token.
func botAPI(t *testing.T, reject bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/getMe") {
			if reject {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"ok":false,"error_code":401,"description":"Unauthorized"}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":4242,"is_bot":true,"first_name":"Fake","username":"fake_bot"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProductionWiringBuildsTheTelegramAdapters(t *testing.T) {
	db := testDB(t)
	rt := New(db, Options{})
	require.NotNil(t, rt.engine, "the agent builds its own workflow engine")
	require.NotNil(t, rt.clients, "and the MTProto adapter")
	require.NotNil(t, rt.factory)
	assert.Contains(t, rt.engine.RegisteredCommandHandlerNames(), "confirm_bind", "Telegram command handlers are installed")
}

func TestProductionBotFactoryTalksToTheBotAPI(t *testing.T) {
	rt := New(testDB(t), Options{Clients: newFakeClients()})
	srv := botAPI(t, false)

	runner, err := rt.factory(model.TelegramBot{ID: 4, Name: "alpha", APIBaseURL: srv.URL, MiniAppURL: "https://app"}, botToken)
	require.NoError(t, err)
	username, id := runner.Identity()
	assert.Equal(t, "fake_bot", username)
	assert.EqualValues(t, 4242, id)

	rejecting := botAPI(t, true)
	_, err = rt.factory(model.TelegramBot{ID: 4, Name: "alpha", APIBaseURL: rejecting.URL}, botToken)
	assert.Error(t, err, "a token Telegram rejects is an error the admin can see")

	_, err = rt.telegramBotFactory(model.TelegramBot{ID: 4, Name: "empty"}, "")
	assert.ErrorContains(t, err, "no usable token")
}

func TestARunningAgentReportsTheIdentityTelegramGaveTheBot(t *testing.T) {
	db := testDB(t)
	srv := botAPI(t, false)
	rt := New(db, Options{Clients: newFakeClients()})
	bot := newFixtureBot(t, rt, srv.URL)

	rt.Reconcile(context.Background())
	got, err := rt.bots.Get(context.Background(), bot.ID)
	require.NoError(t, err)
	assert.Equal(t, model.TelegramBotRunning, got.Status)
	assert.Equal(t, "fake_bot", got.Username)
	assert.EqualValues(t, 4242, got.BotUserID)
	rt.shutdown()
	got, _ = rt.bots.Get(context.Background(), bot.ID)
	assert.Equal(t, model.TelegramBotStopped, got.Status)
}

func newFixtureBot(t *testing.T, rt *Runtime, apiURL string) *model.TelegramBot {
	t.Helper()
	name, token := "alpha", botToken
	bot, err := rt.bots.Create(context.Background(), rdb.TelegramBotInput{Name: &name, Token: &token, APIBaseURL: &apiURL})
	require.NoError(t, err)
	return bot
}
