package telegramagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func addCached(t *testing.T, f *fixture, id int, status string, created time.Time, consumedAt *time.Time) {
	t.Helper()
	row := model.TelegramClientMessage{
		AccountID: 1, PeerID: 1, TelegramMessageID: id, SourceType: model.TelegramSourceChannel,
		MessageAt: created, Status: status, ConsumedAt: consumedAt,
	}
	row.SetText(fmt.Sprint("m", id))
	require.NoError(t, f.db.Create(&row).Error)
	require.NoError(t, f.db.Model(&row).UpdateColumn("created_at", created).Error)
}

func cachedIDs(t *testing.T, f *fixture) []int {
	t.Helper()
	var rows []model.TelegramClientMessage
	require.NoError(t, f.db.Order("telegram_message_id").Find(&rows).Error)
	ids := []int{}
	for _, row := range rows {
		ids = append(ids, row.TelegramMessageID)
	}
	return ids
}

func TestCleanupDeletesConsumedMessagesAfterAGraceAndExpiredOnesAfterTheTTL(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	now := time.Now()
	old := now.Add(-time.Hour)
	recent := now.Add(-time.Minute)
	addCached(t, f, 1, model.TelegramMsgConsumed, now.Add(-time.Hour), &old)      // consumed long ago: gone
	addCached(t, f, 2, model.TelegramMsgConsumed, now.Add(-time.Minute), &recent) // just consumed: still visible
	addCached(t, f, 3, model.TelegramMsgPending, now.Add(-time.Hour), nil)        // waiting: kept
	addCached(t, f, 4, model.TelegramMsgPending, now.Add(-25*time.Hour), nil)     // older than the default TTL: gone
	addCached(t, f, 5, model.TelegramMsgFailed, now.Add(-30*time.Hour), nil)      // failed and expired: gone
	addCached(t, f, 6, model.TelegramMsgFailed, now.Add(-2*time.Hour), nil)       // failed, kept for inspection

	f.rt.Cleanup(ctx, now)

	assert.Equal(t, []int{2, 3, 6}, cachedIDs(t, f))
	job := f.rt.board.Snapshot()[2]
	assert.Equal(t, JobCleanup, job.Name)
	assert.Equal(t, "deleted 1 consumed, 2 expired", job.Detail)
	assert.Empty(t, job.LastError)

	// A shorter TTL from the settings expires the waiting message too.
	require.NoError(t, f.db.Create(&model.SysConfig{ConfigKey: ConfigCacheTTLHours, ConfigValue: "1", ConfigName: "ttl"}).Error)
	f.rt.config = mapConfig{ConfigCacheTTLHours: "1"}
	f.rt.Cleanup(ctx, now.Add(10*time.Minute))
	assert.Empty(t, cachedIDs(t, f), "everything left is now older than the TTL or consumed past the grace")
}

func TestTheCacheTTLComesFromTheSettings(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for value, want := range map[string]time.Duration{"6": 6 * time.Hour, "": defaultCacheTTL, "abc": defaultCacheTTL, "0": defaultCacheTTL, "-3": defaultCacheTTL} {
		f.rt.config = mapConfig{ConfigCacheTTLHours: value}
		assert.Equal(t, want, f.rt.cacheTTL(ctx), "ttl %q", value)
	}
	f.rt.config = failingConfig{}
	assert.Equal(t, defaultCacheTTL, f.rt.cacheTTL(ctx))
	f.rt.config = nil
	assert.Equal(t, defaultCacheTTL, f.rt.cacheTTL(ctx))
}

func TestTheHeartbeatDescribesTheAgent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := f.addBot("alpha", true)
	require.NotNil(t, a)
	acct := model.TelegramClientAccount{Name: "a", PhoneNumber: "+1", APIID: 1, APIHashEncrypted: "x", SessionEncrypted: "s", Authorized: true, Enabled: true}
	require.NoError(t, f.db.Create(&acct).Error)
	addCached(t, f, 1, model.TelegramMsgPending, time.Now(), nil)
	addCached(t, f, 2, model.TelegramMsgPending, time.Now(), nil)
	f.rt.board.Record(JobForward, errors.New("kafka down"), "2 cached, 0 consumed")

	f.rt.Reconcile(ctx)

	rows, total, err := f.rt.statusDAO.List(ctx, 1, 10, rdb.TelegramAgentFilter{})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	st := rows[0]
	assert.Equal(t, "agent-a", st.AgentID)
	assert.Equal(t, AgentVersion, st.Version)
	assert.True(t, st.Online)
	assert.EqualValues(t, 1, st.Accounts)
	assert.EqualValues(t, 1, st.Bots)
	assert.EqualValues(t, 2, st.Pending)
	assert.Contains(t, st.LastError, "kafka down")
	var jobs []JobState
	require.NoError(t, json.Unmarshal([]byte(st.JobsJSON), &jobs))
	require.Len(t, jobs, 4)
	assert.Equal(t, []string{JobForward, JobConsumers, JobCleanup, JobConnections}, []string{jobs[0].Name, jobs[1].Name, jobs[2].Name, jobs[3].Name})
	assert.Equal(t, "1 of 1 accounts connected", jobs[3].Detail)
	assert.Equal(t, "agent-a", f.clients.agent, "the client manager knows which agent it runs for")

	// The next heartbeat updates the same row.
	f.rt.Reconcile(ctx)
	_, total, _ = f.rt.statusDAO.List(ctx, 1, 10, rdb.TelegramAgentFilter{})
	assert.EqualValues(t, 1, total)
}

func TestAnAgentOnlyRunsAccountsNoOtherAgentOwns(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	mk := func(name, agent string) model.TelegramClientAccount {
		row := model.TelegramClientAccount{Name: name, PhoneNumber: name, APIID: 1, APIHashEncrypted: "x", SessionEncrypted: "s", Authorized: true, Enabled: true, AgentID: agent}
		require.NoError(t, f.db.Create(&row).Error)
		return row
	}
	free, mine, theirs := mk("+1", ""), mk("+2", "agent-a"), mk("+3", "agent-b")

	f.rt.Reconcile(ctx)

	assert.True(t, f.clients.called(fmt.Sprintf("connect:%d", free.ID)), "an unclaimed account is taken")
	assert.True(t, f.clients.called(fmt.Sprintf("connect:%d", mine.ID)))
	assert.False(t, f.clients.called(fmt.Sprintf("connect:%d", theirs.ID)), "an account bound to another agent is left alone")
}

func TestARunningAgentConsumesCachedMessagesEndToEnd(t *testing.T) {
	f := newFixture(t)
	f.rt.forwardEvery = 20 * time.Millisecond
	queue := legacyRule(t, f.db, model.TelegramForwardRule{Name: "queue", Enabled: true, TargetKind: model.ForwardTargetRedis, TargetRef: "q"})
	require.NoError(t, f.db.Create(&queue).Error)
	row := model.TelegramClientMessage{AccountID: 1, PeerID: 1, TelegramMessageID: 1, SourceType: model.TelegramSourceChannel, Text: "hi", MessageAt: time.Now(), Status: model.TelegramMsgPending}
	require.NoError(t, f.db.Create(&row).Error)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.rt.Run(ctx) }()
	require.Eventually(t, func() bool { return len(f.pub.all()) == 1 }, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		var got model.TelegramClientMessage
		f.db.First(&got, row.ID)
		return got.Status == model.TelegramMsgConsumed
	}, 5*time.Second, 10*time.Millisecond)
	cancel()
	require.NoError(t, <-done)
	assert.True(t, f.pub.closed, "the channel connections are closed on shutdown")
}

func TestDefaultAgentIDIsTheHostName(t *testing.T) {
	assert.NotEmpty(t, DefaultAgentID())
}

func TestJobBoardKeepsTheLatestStateOfEveryJob(t *testing.T) {
	b := newJobBoard()
	assert.Empty(t, b.LastError())
	b.Record("custom", errors.New("boom"), "d")
	b.Record(JobCleanup, nil, "ok")
	b.Record(JobCleanup, errors.New("later failure"), "")
	snap := b.Snapshot()
	require.Len(t, snap, 4, "unknown jobs are tracked but only the four named jobs are reported")
	assert.EqualValues(t, 2, snap[2].Runs)
	assert.EqualValues(t, 1, snap[2].Failures)
	assert.Contains(t, b.LastError(), "failure")
	assert.Equal(t, "[]", (&jobBoard{jobs: map[string]*JobState{}}).JSON())
}
