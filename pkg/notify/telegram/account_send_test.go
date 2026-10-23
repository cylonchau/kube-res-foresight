package telegram

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/notify"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
	"time"
)

func accountNotifier(t *testing.T, status string) (*Notifier, *rdb.TelegramAgentRequestDAO, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:acctsend"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.TelegramClientAccount{}, &model.TelegramAgentRequest{}, &model.SysConfig{}, &model.SystemMarker{}))
	acc := &model.TelegramClientAccount{PhoneNumber: "+100", APIID: 1, Enabled: true, Authorized: true, ConnectionStatus: status}
	require.NoError(t, db.Create(acc).Error)
	requests := rdb.NewTelegramAgentRequestDAO(db)
	n := &Notifier{configDAO: rdb.NewSysConfigDAO(db)}
	WithClientAccount(n, rdb.NewTelegramClientDAO(db), requests)
	return n, requests, db
}

func TestSendGoesThroughTheConnectedUserAccountWithoutABot(t *testing.T) {
	n, requests, _ := accountNotifier(t, "connected")
	done := make(chan *model.TelegramAgentRequest, 1)
	go func() {
		for {
			req, _ := requests.Claim(context.Background())
			if req != nil {
				done <- req
				_ = requests.Finish(context.Background(), req.ID, nil, nil)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	require.NoError(t, n.Send(context.Background(), "123456789", notify.Message{Content: "hi"}))
	req := <-done
	assert.Equal(t, model.AgentReqSendMessage, req.Kind)
	var p struct{ Recipient, Text string }
	require.NoError(t, requests.Payload(req, &p))
	assert.Equal(t, "123456789", p.Recipient)
}

func TestSendReportsTheAgentFailure(t *testing.T) {
	n, requests, _ := accountNotifier(t, "connected")
	go func() {
		for {
			req, _ := requests.Claim(context.Background())
			if req != nil {
				_ = requests.Finish(context.Background(), req.ID, nil, assert.AnError)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	assert.ErrorContains(t, n.Send(context.Background(), "123456789", notify.Message{Content: "hi"}), "account #")
}

func TestSendWithoutAConnectedAccountOrBotFails(t *testing.T) {
	n, _, _ := accountNotifier(t, "disconnected")
	assert.ErrorIs(t, n.Send(context.Background(), "123456789", notify.Message{Content: "hi"}), ErrNoBot)
}

func TestSendUsesTheChosenAccountAndFailsWhenItIsNotConnected(t *testing.T) {
	n, requests, db := accountNotifier(t, "connected")
	other := &model.TelegramClientAccount{PhoneNumber: "+200", APIID: 1, Enabled: true, Authorized: true, ConnectionStatus: "disconnected"}
	require.NoError(t, db.Create(other).Error)
	err := n.Send(context.Background(), "123456789", notify.Message{Content: "hi", SendAccountID: other.ID})
	assert.ErrorContains(t, err, "is not connected", "no silent switch to another account")

	go func() {
		for {
			req, _ := requests.Claim(context.Background())
			if req != nil {
				assert.Equal(t, int64(1), req.TargetID)
				_ = requests.Finish(context.Background(), req.ID, nil, nil)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	require.NoError(t, n.Send(context.Background(), "123456789", notify.Message{Content: "hi", SendAccountID: 1}))
}

func TestALimitedAccountIsPassedOverAndAChosenOneIsRefusedWithTheReason(t *testing.T) {
	n, _, db := accountNotifier(t, "connected")
	until := time.Now().Add(time.Hour)
	require.NoError(t, db.Model(&model.TelegramClientAccount{}).Where("phone_number = ?", "+100").
		Updates(map[string]any{"send_blocked_until": until, "send_blocked_reason": "PEER_FLOOD"}).Error)

	// automatic choice: the limited account is not used, and with no other account and no bot there is nothing to send with
	assert.ErrorIs(t, n.Send(context.Background(), "123456789", notify.Message{Content: "hi"}), ErrNoBot)

	// a chosen account is refused at once with the reason, without a request to the agent
	var acc model.TelegramClientAccount
	require.NoError(t, db.Where("phone_number = ?", "+100").First(&acc).Error)
	err := n.Send(context.Background(), "123456789", notify.Message{Content: "hi", SendAccountID: acc.ID})
	assert.ErrorContains(t, err, "cannot send until")
	assert.ErrorContains(t, err, "PEER_FLOOD")
	var queued int64
	require.NoError(t, db.Model(&model.TelegramAgentRequest{}).Count(&queued).Error)
	assert.Zero(t, queued)

	// another connected account takes over the automatic choice
	other := &model.TelegramClientAccount{PhoneNumber: "+300", APIID: 1, Enabled: true, Authorized: true, ConnectionStatus: "connected"}
	require.NoError(t, db.Create(other).Error)
	got, err := rdb.NewTelegramClientDAO(db).FirstConnectedAccount(context.Background(), 0)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, other.ID, got.ID)
}
