package telegramagent

import (
	"context"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestARequestForAnAccountIsTakenOnlyByTheAgentThatRunsIt(t *testing.T) {
	f := newFixture(t) // this agent is "agent-a"
	ctx := context.Background()
	mk := func(phone, agent string) model.TelegramClientAccount {
		a := model.TelegramClientAccount{Name: phone, PhoneNumber: phone, APIID: 1, APIHashEncrypted: "x", Authorized: true, Enabled: true, SessionEncrypted: "s", AgentID: agent}
		require.NoError(t, f.db.Create(&a).Error)
		return a
	}
	mine, theirs, free := mk("+1", "agent-a"), mk("+2", "agent-b"), mk("+3", "")
	sub := model.TelegramClientSubscription{AccountID: theirs.ID, PeerID: 9, PeerType: "channel", Enabled: true, State: model.TelegramSubJoined}
	require.NoError(t, f.db.Create(&sub).Error)

	q := f.rt.requests
	enqueue := func(kind string, target int64) int64 {
		row, err := q.Enqueue(ctx, kind, target, nil)
		require.NoError(t, err)
		return row.ID
	}
	forTheirs := enqueue(model.AgentReqConnect, theirs.ID)
	leaveTheirs := enqueue(model.AgentReqUnsubscribe, sub.ID)
	forMine := enqueue(model.AgentReqConnect, mine.ID)
	forFree := enqueue(model.AgentReqConnect, free.ID)
	bot := enqueue(model.AgentReqSyncBotCommand, 404)

	f.rt.drainRequests(ctx)

	status := func(id int64) string {
		row, err := q.Get(ctx, id)
		require.NoError(t, err)
		return row.Status
	}
	assert.Equal(t, model.AgentReqPending, status(forTheirs), "agent-b's account is left for agent-b")
	assert.Equal(t, model.AgentReqPending, status(leaveTheirs), "so is leaving one of its channels")
	assert.NotEqual(t, model.AgentReqPending, status(forMine))
	assert.NotEqual(t, model.AgentReqPending, status(forFree), "an account nobody runs yet can be taken by anyone")
	assert.NotEqual(t, model.AgentReqPending, status(bot), "bot requests belong to no particular agent")
	assert.True(t, f.clients.called(fmt.Sprintf("connect:%d", mine.ID)))
	assert.False(t, f.clients.called(fmt.Sprintf("connect:%d", theirs.ID)))
}
