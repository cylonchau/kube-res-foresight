package telegramclient

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sort"
	"testing"
	"time"
)

// historyInvoker answers messages.getHistory from a fixed list of channel messages, newest first,
// the way Telegram does (min_id, offset_id and limit).
type historyInvoker struct {
	ids   []int
	calls int
}

func (h *historyInvoker) Invoke(_ context.Context, input bin.Encoder, output bin.Decoder) error {
	req, ok := input.(*tg.MessagesGetHistoryRequest)
	if !ok {
		return nil
	}
	h.calls++
	ids := append([]int(nil), h.ids...)
	sort.Sort(sort.Reverse(sort.IntSlice(ids)))
	var out []tg.MessageClass
	for _, id := range ids {
		if req.MinID > 0 && id <= req.MinID {
			continue
		}
		if req.OffsetID > 0 && id >= req.OffsetID {
			continue
		}
		out = append(out, &tg.Message{ID: id, PeerID: &tg.PeerChannel{ChannelID: 777}, Message: "m", Date: id})
		if len(out) == req.Limit {
			break
		}
	}
	var buf bin.Buffer
	if err := (&tg.MessagesChannelMessages{Messages: out}).Encode(&buf); err != nil {
		return err
	}
	return output.Decode(&buf)
}

func cachedTotal(t *testing.T, m *Manager) int64 {
	t.Helper()
	req := &apis.TelegramClientMessageListReq{}
	req.Page, req.PageSize = 1, 1
	_, total, err := m.dao.ListMessages(context.Background(), req)
	require.NoError(t, err)
	return total
}

func joinedChannel(t *testing.T, m *Manager, position int) model.TelegramClientSubscription {
	t.Helper()
	sub := &model.TelegramClientSubscription{AccountID: 3, PeerID: 777, AccessHash: 5, PeerType: "channel", Username: "news", Enabled: true, State: model.TelegramSubJoined, LastMessageID: position}
	require.NoError(t, m.dao.CreateSubscription(context.Background(), sub))
	return *sub
}

func TestCatchUpTakesInWhatWasSentWhileTheAgentWasStopped(t *testing.T) {
	dao := newTelegramClientTestDAO(t)
	manager := &Manager{dao: dao, clients: make(map[int64]*runningClient)}
	events, _ := collectEvents(manager)
	sub := joinedChannel(t, manager, 10)

	// 250 messages exist; ids 11..250 arrived while the agent was down.
	var ids []int
	for i := 1; i <= 250; i++ {
		ids = append(ids, i)
	}
	api := tg.NewClient(&historyInvoker{ids: ids})
	require.NoError(t, manager.catchUpOne(context.Background(), 3, api, sub))

	assert.EqualValues(t, 240, cachedTotal(t, manager), "every message after the read position is cached, none before it")
	got, err := dao.GetSubscription(context.Background(), sub.ID)
	require.NoError(t, err)
	assert.Equal(t, 250, got.LastMessageID, "the position follows the newest message taken in")
	assert.Len(t, events(), 240)

	// A second start finds nothing new and takes nothing twice.
	require.NoError(t, manager.catchUpOne(context.Background(), 3, api, *got))
	assert.EqualValues(t, 240, cachedTotal(t, manager))
}

func TestCatchUpWithoutAPositionStartsFromNowAndDoesNotReplayHistory(t *testing.T) {
	dao := newTelegramClientTestDAO(t)
	manager := &Manager{dao: dao, clients: make(map[int64]*runningClient)}
	sub := joinedChannel(t, manager, 0)

	api := tg.NewClient(&historyInvoker{ids: []int{1, 2, 3, 4, 5}})
	require.NoError(t, manager.catchUpOne(context.Background(), 3, api, sub))

	assert.Empty(t, cachedMessages(t, dao), "a first start does not forward the chat's old messages")
	got, _ := dao.GetSubscription(context.Background(), sub.ID)
	assert.Equal(t, 5, got.LastMessageID)
}

func TestCatchUpFollowsWhatIsStillCachedForSubscriptionsFromBeforePositionsExisted(t *testing.T) {
	dao := newTelegramClientTestDAO(t)
	manager := &Manager{dao: dao, clients: make(map[int64]*runningClient)}
	sub := joinedChannel(t, manager, 0)
	require.NoError(t, dao.SaveMessage(context.Background(), &model.TelegramClientMessage{
		AccountID: 3, SubscriptionID: sub.ID, PeerID: 777, TelegramMessageID: 3, SourceType: model.TelegramSourceChannel, Status: model.TelegramMsgPending,
	}))

	api := tg.NewClient(&historyInvoker{ids: []int{1, 2, 3, 4, 5}})
	require.NoError(t, manager.catchUpOne(context.Background(), 3, api, sub))

	var ids []int
	for _, r := range cachedMessages(t, dao) {
		ids = append(ids, r.TelegramMessageID)
	}
	sort.Ints(ids)
	assert.Equal(t, []int{3, 4, 5}, ids, "continues after the newest message that was already cached")
}

func TestLiveMessagesDuringCatchUpDoNotMoveThePositionPastTheMissingOnes(t *testing.T) {
	dao := newTelegramClientTestDAO(t)
	manager := &Manager{dao: dao, clients: make(map[int64]*runningClient), catching: map[int64]int{}}
	sub := joinedChannel(t, manager, 10)
	manager.catching[sub.ID] = 0 // a catch-up is running

	require.NoError(t, manager.handleMessage(context.Background(), 3, tg.Entities{}, &tg.Message{ID: 90, PeerID: &tg.PeerChannel{ChannelID: 777}, Message: "live", Date: 1}))
	got, _ := dao.GetSubscription(context.Background(), sub.ID)
	assert.Equal(t, 10, got.LastMessageID, "messages 11..89 are still to be read, so the position stays")
	assert.Len(t, cachedMessages(t, dao), 1, "the live message itself is cached")
}

func TestAMessageFilteredOutByKeywordsStillCountsAsRead(t *testing.T) {
	dao := newTelegramClientTestDAO(t)
	manager := &Manager{dao: dao, clients: make(map[int64]*runningClient)}
	sub := &model.TelegramClientSubscription{AccountID: 3, PeerID: 777, PeerType: "channel", Username: "news", Enabled: true, State: model.TelegramSubJoined, Keywords: `["btc"]`}
	require.NoError(t, dao.CreateSubscription(context.Background(), sub))

	require.NoError(t, manager.handleMessage(context.Background(), 3, tg.Entities{}, &tg.Message{ID: 42, PeerID: &tg.PeerChannel{ChannelID: 777}, Message: "weather", Date: 1}))
	got, _ := dao.GetSubscription(context.Background(), sub.ID)
	assert.Equal(t, 42, got.LastMessageID)
	assert.Empty(t, cachedMessages(t, dao))
}

func nextAttempts(t *testing.T, m *Manager) []time.Time {
	t.Helper()
	req := &apis.TelegramClientMessageListReq{}
	req.Page, req.PageSize = 1, 100
	rows, _, err := m.dao.ListMessages(context.Background(), req)
	require.NoError(t, err)
	sort.Slice(rows, func(i, j int) bool { return rows[i].TelegramMessageID < rows[j].TelegramMessageID })
	var out []time.Time
	for _, r := range rows {
		if r.NextAttemptAt == nil {
			out = append(out, time.Time{})
		} else {
			out = append(out, *r.NextAttemptAt)
		}
	}
	return out
}

func TestABacklogIsForwardedOneByOneWithADelayAndAJitter(t *testing.T) {
	dao := newTelegramClientTestDAO(t)
	manager := &Manager{
		dao: dao, clients: make(map[int64]*runningClient),
		policy: func(context.Context) CatchUpPolicy {
			return CatchUpPolicy{MinBacklog: 2, Delay: 10 * time.Second, Jitter: 4 * time.Second}
		},
		random: func(n int64) int64 { return 2 }, // a fixed jitter of 2 seconds
	}
	sub := joinedChannel(t, manager, 0)
	require.NoError(t, dao.AdvanceSubscriptionPosition(context.Background(), sub.ID, 10))
	sub.LastMessageID = 10

	api := tg.NewClient(&historyInvoker{ids: []int{9, 10, 11, 12, 13, 14}})
	before := time.Now()
	require.NoError(t, manager.catchUpOne(context.Background(), 3, api, sub))

	at := nextAttempts(t, manager)
	require.Len(t, at, 4, "messages 11..14")
	assert.True(t, at[0].IsZero(), "the first one goes out at once")
	for i := 1; i < len(at); i++ {
		gap := at[i].Sub(at[i-1])
		if i == 1 {
			gap = at[1].Sub(before)
			assert.InDelta(t, 12, gap.Seconds(), 1.5, "base 10s plus the 2s jitter")
			continue
		}
		assert.InDelta(t, 12, gap.Seconds(), 0.5, "each later message waits 12s after the one before")
	}
	got, _ := dao.GetSubscription(context.Background(), sub.ID)
	assert.Equal(t, 14, got.LastMessageID)
	assert.Equal(t, 14, got.LatestMessageID, "the newest message seen is recorded next to the read position")
}

func TestAShortGapIsForwardedAtOnce(t *testing.T) {
	dao := newTelegramClientTestDAO(t)
	manager := &Manager{dao: dao, clients: make(map[int64]*runningClient),
		policy: func(context.Context) CatchUpPolicy { return CatchUpPolicy{MinBacklog: 3, Delay: time.Minute} }}
	sub := joinedChannel(t, manager, 10)
	api := tg.NewClient(&historyInvoker{ids: []int{9, 10, 11, 12}})
	require.NoError(t, manager.catchUpOne(context.Background(), 3, api, sub))
	for _, at := range nextAttempts(t, manager) {
		assert.True(t, at.IsZero(), "two messages are below the configured minimum of three")
	}
}

func TestCatchUpPolicyOffsetsAndSettings(t *testing.T) {
	p := CatchUpPolicy{MinBacklog: 2, Delay: 30 * time.Second, Jitter: 0}
	assert.Equal(t, []time.Duration{0}, p.Offsets(1, nil), "one message needs no spacing")
	assert.Equal(t, []time.Duration{0, 30 * time.Second, 60 * time.Second}, p.Offsets(3, func(int64) int64 { return 0 }))
	assert.Equal(t, []time.Duration{0, 0}, CatchUpPolicy{MinBacklog: 5, Delay: time.Minute}.Offsets(2, nil), "below the minimum: no delay")
	assert.Equal(t, []time.Duration{0, 0, 0}, CatchUpPolicy{MinBacklog: 2}.Offsets(3, nil), "no delay configured: no spacing")

	long := CatchUpPolicy{MinBacklog: 2, Delay: time.Minute}.Offsets(5000, func(int64) int64 { return 0 })
	assert.LessOrEqual(t, long[len(long)-1], maxCatchUpSpan, "a long backlog is compressed to fit the cache lifetime")

	src := mapSource{ConfigCatchUpMinBacklog: "4", ConfigCatchUpDelay: "5", ConfigCatchUpJitter: "-"}
	got := LoadCatchUpPolicy(context.Background(), src)
	assert.Equal(t, 4, got.MinBacklog)
	assert.Equal(t, 5*time.Second, got.Delay)
	assert.Equal(t, DefaultCatchUpPolicy.Jitter, got.Jitter, "a dash means unset")
	assert.Equal(t, DefaultCatchUpPolicy, LoadCatchUpPolicy(context.Background(), nil))
}

type mapSource map[string]string

func (m mapSource) GetValueOrDefault(_ context.Context, key, fallback string) (string, error) {
	if v, ok := m[key]; ok {
		return v, nil
	}
	return fallback, nil
}
