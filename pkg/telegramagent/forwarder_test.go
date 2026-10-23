package telegramagent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/job/tasks/notifyjob"
	"github.com/cylonchau/kubeforecast/pkg/message"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"sync"
	"testing"
	"time"
)

type published struct {
	Kind, Destination, Key string
	Payload                []byte
}

type fakePublisher struct {
	mu    sync.Mutex
	calls []published
	err   error
	// failFor makes only the destination of that name fail.
	failFor string
	closed  bool
}

func (p *fakePublisher) Publish(_ context.Context, ch model.TelegramForwardChannel, key string, payload []byte) error {
	kind, destination := "redis", ch.Stream
	if ch.Transport == model.ChannelTransportKafka {
		kind, destination = "kafka", ch.Topic
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	if p.failFor != "" && p.failFor == destination {
		return errors.New("channel unavailable")
	}
	p.calls = append(p.calls, published{kind, destination, key, payload})
	return nil
}
func (p *fakePublisher) Close() { p.mu.Lock(); p.closed = true; p.mu.Unlock() }
func (p *fakePublisher) all() []published {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]published(nil), p.calls...)
}

type forwardFixture struct {
	t   *testing.T
	db  *gorm.DB
	f   *Forwarder
	pub *fakePublisher
	at  time.Time
}

func newForwardFixture(t *testing.T) *forwardFixture {
	t.Helper()
	db := testDB(t)
	engine := workflow.NewEngine(rdb.NewWorkflowStore(db), rdb.NewNotifyTemplateDAO(db), rdb.NewTelegramBindingDAO(db), nil)
	pipeline, err := json.Marshal(workflow.Pipeline{InitialState: "new", States: map[string]workflow.StateDef{"new": {IsTerminal: true}}})
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.WorkflowDef{WorkflowKey: "review", Name: "Review", InitialState: "new", PipelineJSON: string(pipeline), Enabled: true}).Error)
	pub := &fakePublisher{}
	fx := &forwardFixture{t: t, db: db, pub: pub, at: time.Now()}
	fx.f = NewForwarder(db, engine, pub, nil)
	fx.f.now = func() time.Time { return fx.at }
	return fx
}

func (fx *forwardFixture) rule(r model.TelegramForwardRule) model.TelegramForwardRule {
	fx.t.Helper()
	r.Enabled = true
	r = legacyRule(fx.t, fx.db, r)
	require.NoError(fx.t, rdb.NewTelegramForwardRuleDAO(fx.db).Create(context.Background(), &r))
	return r
}

var nextMsgID = 1000

func (fx *forwardFixture) cache(account, subscription int64, sourceType, text string) model.TelegramClientMessage {
	fx.t.Helper()
	nextMsgID++
	row := model.TelegramClientMessage{
		AccountID: account, SubscriptionID: subscription, PeerID: 77, TelegramMessageID: nextMsgID, SourceType: sourceType,
		Source: "newsfeed", SenderName: "Alice", MediaType: "text", MessageAt: fx.at, Status: model.TelegramMsgPending,
	}
	row.SetText(text) // the cache stores the text compressed
	require.NoError(fx.t, fx.db.Create(&row).Error)
	return row
}

func (fx *forwardFixture) reload(id int64) model.TelegramClientMessage {
	fx.t.Helper()
	var row model.TelegramClientMessage
	require.NoError(fx.t, fx.db.First(&row, id).Error)
	return row
}

func countJobs(t *testing.T, db *gorm.DB, jobType string) int {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&model.Job{}).Where("job_type = ?", jobType).Count(&n).Error)
	return int(n)
}

func TestACachedMessageIsDeliveredToEveryTargetKindAndConsumed(t *testing.T) {
	fx := newForwardFixture(t)
	ctx := context.Background()
	fx.rule(model.TelegramForwardRule{Name: "notify", TargetKind: model.ForwardTargetNotify, TargetRef: "telegram", TargetRecipient: "42"})
	fx.rule(model.TelegramForwardRule{Name: "rate", TargetKind: model.ForwardTargetJob, TargetRef: "llm_rating"})
	fx.rule(model.TelegramForwardRule{Name: "flow", TargetKind: model.ForwardTargetWorkflow, TargetRef: "review"})
	fx.rule(model.TelegramForwardRule{Name: "queue", TargetKind: model.ForwardTargetRedis, TargetRef: "tg:messages"})
	fx.rule(model.TelegramForwardRule{Name: "topic", TargetKind: model.ForwardTargetKafka, TargetRef: "tg-messages"})
	row := fx.cache(1, 5, model.TelegramSourceChannel, "BTC breaks out")

	require.Equal(t, 1, fx.f.Pass(ctx))

	got := fx.reload(row.ID)
	assert.Equal(t, model.TelegramMsgConsumed, got.Status)
	assert.NotNil(t, got.ConsumedAt)
	assert.Equal(t, 1, countJobs(t, fx.db, notifyjob.JobType))
	assert.Equal(t, 1, countJobs(t, fx.db, "llm_rating"))
	var instances int64
	fx.db.Model(&model.WorkflowInstance{}).Count(&instances)
	assert.EqualValues(t, 1, instances)

	calls := fx.pub.all()
	require.Len(t, calls, 2)
	assert.Equal(t, "redis", calls[0].Kind)
	assert.Equal(t, "tg:messages", calls[0].Destination)
	assert.Equal(t, "newsfeed", calls[0].Key, "one source stays ordered on Kafka")
	assert.Equal(t, "kafka", calls[1].Kind)
	var std message.Message
	require.NoError(t, json.Unmarshal(calls[0].Payload, &std))
	assert.Equal(t, "BTC breaks out", std.Text)
	assert.Equal(t, "telegram", std.Platform)
	assert.Equal(t, model.TelegramSourceChannel, std.SourceType)
	assert.Equal(t, "newsfeed", std.Source)

	assert.EqualValues(t, 5, fx.f.Forwarded())
	assert.Zero(t, fx.f.Failed())
	assert.Zero(t, fx.f.Pass(ctx), "a consumed message is not picked up again")
	job := fx.f.board.Snapshot()[0]
	assert.Equal(t, JobForward, job.Name)
	assert.EqualValues(t, 1, job.Runs)
	assert.Contains(t, job.Detail, "1 consumed")
}

func TestRulesApplyOnlyToTheirSource(t *testing.T) {
	fx := newForwardFixture(t)
	ctx := context.Background()
	fx.rule(model.TelegramForwardRule{Name: "all", TargetKind: model.ForwardTargetRedis, TargetRef: "all"})
	fx.rule(model.TelegramForwardRule{Name: "acct2", AccountID: 2, TargetKind: model.ForwardTargetRedis, TargetRef: "acct2"})
	fx.rule(model.TelegramForwardRule{Name: "sub9", SubscriptionID: 9, TargetKind: model.ForwardTargetRedis, TargetRef: "sub9"})
	fx.rule(model.TelegramForwardRule{Name: "dm", SourceType: model.TelegramSourcePrivate, TargetKind: model.ForwardTargetRedis, TargetRef: "dm"})
	fx.rule(model.TelegramForwardRule{Name: "btc", Pattern: "^BTC", TargetKind: model.ForwardTargetRedis, TargetRef: "btc"})

	fx.cache(1, 5, model.TelegramSourceChannel, "BTC up")
	fx.cache(2, 9, model.TelegramSourceGroup, "ETH up")
	fx.cache(1, 0, model.TelegramSourcePrivate, "hello")
	require.Equal(t, 3, fx.f.Pass(ctx))

	got := map[string]int{}
	for _, c := range fx.pub.all() {
		got[c.Destination]++
	}
	assert.Equal(t, map[string]int{"all": 3, "acct2": 1, "sub9": 1, "dm": 1, "btc": 1}, got)
}

func TestOneMessageGoesToEveryEnabledChannelAndASlowChannelDoesNotRepeatTheOthers(t *testing.T) {
	fx := newForwardFixture(t)
	ctx := context.Background()
	fx.rule(model.TelegramForwardRule{Name: "channel A", TargetKind: model.ForwardTargetRedis, TargetRef: "channel:a"})
	fx.rule(model.TelegramForwardRule{Name: "channel B", TargetKind: model.ForwardTargetRedis, TargetRef: "channel:b"})
	off := fx.rule(model.TelegramForwardRule{Name: "channel C", TargetKind: model.ForwardTargetRedis, TargetRef: "channel:c"})
	require.NoError(t, fx.db.Model(&model.TelegramForwardRule{}).Where("id = ?", off.ID).UpdateColumn("enabled", false).Error)
	fx.pub.failFor = "channel:b"
	row := fx.cache(1, 5, model.TelegramSourceChannel, "BTC breaks out")

	fx.f.Pass(ctx)
	assert.Equal(t, model.TelegramMsgPending, fx.reload(row.ID).Status, "B failed, so the message waits for a retry")
	var destinations []string
	for _, call := range fx.pub.all() {
		destinations = append(destinations, call.Destination)
	}
	assert.Equal(t, []string{"channel:a"}, destinations, "A got it, the disabled C did not")

	fx.pub.failFor = ""
	fx.at = fx.at.Add(time.Minute)
	fx.f.Pass(ctx)
	assert.Equal(t, model.TelegramMsgConsumed, fx.reload(row.ID).Status)
	destinations = nil
	for _, call := range fx.pub.all() {
		destinations = append(destinations, call.Destination)
	}
	assert.Equal(t, []string{"channel:a", "channel:b"}, destinations, "the retry only delivers what is missing")
}
