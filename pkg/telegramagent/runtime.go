// Package telegramagent is the TG Agent: the one process that talks to
// Telegram. It holds the MTProto sessions of the client accounts, receives
// their messages into a temporary cache, forwards them according to the
// forward rules (Redis, Kafka, Notify, Job, Workflow), deletes them once
// consumed, and executes the requests the admin API queues (login, join,
// leave). It also runs the Bot API bots in the same process for now.
//
// It only owns Telegram work. Filtering, LLM processing and pipelines happen
// downstream, through the Job, Notify and Workflow modules or the consumers of
// the Redis / Kafka channels. Messages are never kept as history.
package telegramagent

import (
	"context"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/config"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/logger"
	"github.com/cylonchau/kubeforecast/pkg/message"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/telegram"
	"github.com/cylonchau/kubeforecast/pkg/telegramclient"
	"github.com/cylonchau/kubeforecast/pkg/workflow"
	"gorm.io/gorm"
	"time"
)

// BotRunner is the Bot API adapter of one bot (implemented by
// telegram.BotService). Nothing outside pkg/telegram sees the bot SDK.
type BotRunner interface {
	Identity() (username string, id int64)
	SyncCommands(specs []telegram.CommandSpec) error
	Start(ctx context.Context)
}

// BotFactory builds the adapter for one bot row.
type BotFactory func(row model.TelegramBot, token string) (BotRunner, error)

// ClientRuntime is the MTProto adapter (implemented by telegramclient.Manager).
type ClientRuntime interface {
	Start(ctx context.Context) error
	StartAccount(accountID int64) error
	StopAccount(accountID int64)
	SendCode(ctx context.Context, accountID int64) error
	VerifyCode(ctx context.Context, accountID int64, code, password string) error
	Logout(ctx context.Context, accountID int64) error
	Subscribe(ctx context.Context, accountID int64, username string, keywords []string, join bool) (*model.TelegramClientSubscription, error)
	Unsubscribe(ctx context.Context, subscriptionID int64) error
	SendMessage(ctx context.Context, accountID int64, recipient, text string) error
	ConnectedIDs() []int64
	SetSink(message.Sink)
	SetAgentID(agentID string)
}

// AgentVersion is reported in the agent heartbeat.
const AgentVersion = "1.0.0"

// Options tunes the loops; zero values take the defaults.
type Options struct {
	ReconcileEvery time.Duration
	RequestEvery   time.Duration
	ForwardEvery   time.Duration
	CleanupEvery   time.Duration
	// AgentID names this agent (default: the host name).
	AgentID    string
	BotFactory BotFactory
	Clients    ClientRuntime
	Engine     *workflow.Engine
	// Publisher delivers to Redis / Kafka (default: the settings-driven publisher).
	Publisher Publisher
	// Config reads the channel and cache settings (default: the SysConfig table).
	Config ConfigSource
}

// Runtime is one Telegram Agent.
type Runtime struct {
	db        *gorm.DB
	bots      *rdb.TelegramBotDAO
	accounts  *rdb.TelegramClientDAO
	requests  *rdb.TelegramAgentRequestDAO
	clients   ClientRuntime
	engine    *workflow.Engine
	factory   BotFactory
	forward   *Forwarder
	consumers *ChannelConsumers
	board     *jobBoard
	publisher Publisher
	config    ConfigSource

	messageDAO   *rdb.TelegramClientDAO
	statusDAO    *rdb.TelegramAgentStatusDAO
	agentID      string
	startedAt    time.Time
	reconcile    time.Duration
	poll         time.Duration
	forwardEvery time.Duration
	cleanupEvery time.Duration

	running  map[int64]*runningBot
	retryAt  map[int64]time.Time
	acctNext map[int64]time.Time
}

// New builds an agent over db. Tests pass fakes through Options; production
// leaves them empty and gets the real Telegram adapters.
func New(db *gorm.DB, opts Options) *Runtime {
	r := &Runtime{
		db:           db,
		bots:         rdb.NewTelegramBotDAO(db),
		accounts:     rdb.NewTelegramClientDAO(db),
		requests:     rdb.NewTelegramAgentRequestDAO(db),
		clients:      opts.Clients,
		engine:       opts.Engine,
		factory:      opts.BotFactory,
		reconcile:    opts.ReconcileEvery,
		poll:         opts.RequestEvery,
		forwardEvery: opts.ForwardEvery,
		cleanupEvery: opts.CleanupEvery,
		agentID:      opts.AgentID,
		publisher:    opts.Publisher,
		config:       opts.Config,
		messageDAO:   rdb.NewTelegramClientDAO(db),
		statusDAO:    rdb.NewTelegramAgentStatusDAO(db),
		board:        newJobBoard(),
		startedAt:    time.Now(),
		running:      map[int64]*runningBot{},
		retryAt:      map[int64]time.Time{},
		acctNext:     map[int64]time.Time{},
	}
	if r.reconcile <= 0 {
		r.reconcile = 5 * time.Second
	}
	if r.poll <= 0 {
		r.poll = 500 * time.Millisecond
	}
	if r.forwardEvery <= 0 {
		r.forwardEvery = time.Second
	}
	if r.cleanupEvery <= 0 {
		r.cleanupEvery = time.Minute
	}
	if r.agentID == "" {
		r.agentID = DefaultAgentID()
	}
	if r.config == nil {
		r.config = rdb.NewSysConfigDAO(db)
	}
	if r.publisher == nil {
		r.publisher = NewChannelPublisher(rdb.NewTelegramForwardChannelDAO(db), platformRedis)
	}
	if r.engine == nil {
		r.engine = workflow.NewEngine(rdb.NewWorkflowStore(db), rdb.NewNotifyTemplateDAO(db), rdb.NewTelegramBindingDAO(db), nil)
		r.engine.SetPendingReplyStore(rdb.NewWorkflowPendingReplyDAO(db))
		telegram.Bootstrap(context.Background(), db, r.engine)
	}
	r.forward = NewForwarder(db, r.engine, r.publisher, r.board)
	r.consumers = NewChannelConsumers(rdb.NewTelegramForwardChannelDAO(db), rdb.NewJobDAO(db), r.engine, platformRedis, r.board, r.agentID)
	if r.clients == nil {
		r.clients = telegramclient.NewManager(db)
	}
	r.clients.SetAgentID(r.agentID)
	r.clients.SetSink(r.forward)
	if r.factory == nil {
		r.factory = r.telegramBotFactory
	}
	return r
}

// telegramBotFactory is the production BotFactory.
func (r *Runtime) telegramBotFactory(row model.TelegramBot, token string) (BotRunner, error) {
	cfg := config.TelegramConfig{Enabled: true, BotToken: token, BotUsername: row.Username, MiniAppURL: row.MiniAppURL, APIBaseURL: row.APIBaseURL}
	service, err := telegram.NewBotService(cfg, telegram.BotDeps{
		BotID:          row.ID,
		BindingMgr:     rdb.NewTelegramBindingDAO(r.db),
		Templates:      rdb.NewNotifyTemplateDAO(r.db),
		WorkflowEngine: r.engine,
		Sink:           r.forward,
	})
	if err != nil {
		return nil, err
	}
	if service == nil {
		return nil, fmt.Errorf("bot %q has no usable token", row.Name)
	}
	return service, nil
}

// Run starts the agent and blocks until ctx is cancelled.
func Run(ctx context.Context) error {
	if model.DB == nil {
		return fmt.Errorf("telegram agent database is not initialized")
	}
	return New(model.DB, Options{}).Run(ctx)
}

// Run drives the reconcile and request loops until ctx ends.
func (r *Runtime) Run(ctx context.Context) error {
	logger.Info("TG Agent starting", logger.String("agent_id", r.agentID))
	if err := r.clients.Start(ctx); err != nil {
		return fmt.Errorf("start Telegram client accounts: %w", err)
	}
	go r.forward.Run(ctx, r.forwardEvery)
	go r.consumers.Run(ctx)

	reconcile := time.NewTicker(r.reconcile)
	defer reconcile.Stop()
	requests := time.NewTicker(r.poll)
	defer requests.Stop()
	sweep := time.NewTicker(time.Minute)
	defer sweep.Stop()
	cleanup := time.NewTicker(r.cleanupEvery)
	defer cleanup.Stop()

	r.Reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			r.shutdown()
			logger.Info("Telegram Agent stopped")
			return nil
		case <-reconcile.C:
			r.Reconcile(ctx)
		case <-requests.C:
			r.drainRequests(ctx)
		case now := <-cleanup.C:
			r.Cleanup(ctx, now)
		case now := <-sweep.C:
			if err := r.requests.Sweep(ctx, now, time.Hour); err != nil {
				logger.Warn("Telegram Agent request sweep failed", logger.Err(err))
			}
		}
	}
}

// Reconcile brings running bots and accounts in line with the database and
// refreshes their heartbeats.
func (r *Runtime) Reconcile(ctx context.Context) {
	r.reconcileBots(ctx)
	r.reconcileAccounts(ctx)
	r.Report(ctx)
}

func (r *Runtime) shutdown() {
	ctx := context.Background()
	for id, bot := range r.running {
		bot.cancel()
		_ = r.bots.MarkStopped(ctx, id)
		delete(r.running, id)
	}
	for _, id := range r.clients.ConnectedIDs() {
		r.clients.StopAccount(id)
	}
	r.publisher.Close()
}

// platformRedis is the Redis of config.yaml, which a channel without an address of its own uses.
func platformRedis() config.RedisConfig {
	if config.CONFIG != nil {
		return config.CONFIG.Redis
	}
	return config.RedisConfig{}
}
