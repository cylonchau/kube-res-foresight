package telegramagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/logger"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/telegram"
	"sort"
	"strings"
	"time"
)

// botRetryDelay keeps a bot that failed to start from being retried on every
// reconcile tick.
const botRetryDelay = 30 * time.Second

// accountRetryDelay spaces reconnect attempts of a client account.
const accountRetryDelay = 30 * time.Second

type runningBot struct {
	id          int64
	fingerprint string
	cancel      context.CancelFunc
	runner      BotRunner
}

// fingerprint changes whenever something the running connection depends on
// changes, so the bot is restarted with the new settings.
func fingerprint(row model.TelegramBot) string {
	sum := sha256.Sum256([]byte(row.TokenEncrypted + "|" + row.APIBaseURL + "|" + row.Mode))
	return hex.EncodeToString(sum[:8])
}

func (r *Runtime) reconcileBots(ctx context.Context) {
	rows, err := r.bots.ListEnabled(ctx)
	if err != nil {
		logger.Warn("Telegram Agent could not list bots", logger.Err(err))
		return
	}
	wanted := make(map[int64]model.TelegramBot, len(rows))
	for _, row := range rows {
		wanted[row.ID] = row
	}

	for id, bot := range r.running {
		row, ok := wanted[id]
		if ok && row.Mode == model.TelegramModePolling && row.TokenEncrypted != "" && bot.fingerprint == fingerprint(row) {
			continue
		}
		bot.cancel()
		delete(r.running, id)
		if err := r.bots.MarkStopped(ctx, id); err != nil {
			logger.Warn("Telegram Agent could not mark a bot stopped", logger.Err(err))
		}
	}

	for _, row := range rows {
		if _, ok := r.running[row.ID]; ok || time.Now().Before(r.retryAt[row.ID]) {
			continue
		}
		r.startBot(ctx, row)
	}

	ids := make([]int64, 0, len(r.running))
	for id := range r.running {
		ids = append(ids, id)
	}
	for _, id := range ids {
		r.syncCommands(ctx, id)
	}
	if err := r.bots.Touch(ctx, ids); err != nil {
		logger.Warn("Telegram Agent could not refresh bot heartbeats", logger.Err(err))
	}
	// A disabled bot that an older run left marked running.
	r.markDisabledStopped(ctx)
}

func (r *Runtime) markDisabledStopped(ctx context.Context) {
	r.db.WithContext(ctx).Model(&model.TelegramBot{}).
		Where("enabled = ? AND status <> ?", false, model.TelegramBotStopped).
		Updates(map[string]any{"status": model.TelegramBotStopped, "started_at": nil})
}

func (r *Runtime) startBot(ctx context.Context, row model.TelegramBot) {
	fail := func(err error) {
		logger.Warn("Telegram bot failed to start", logger.Int64("bot_id", row.ID), logger.Err(err))
		r.retryAt[row.ID] = time.Now().Add(botRetryDelay)
		_ = r.bots.MarkError(ctx, row.ID, err.Error())
	}
	if row.Mode != model.TelegramModePolling {
		fail(errUnsupportedMode)
		return
	}
	token, err := r.bots.Token(&row)
	if err != nil {
		fail(err)
		return
	}
	runner, err := r.factory(row, token)
	if err != nil {
		fail(err)
		return
	}
	botCtx, cancel := context.WithCancel(ctx)
	runner.Start(botCtx)
	r.running[row.ID] = &runningBot{id: row.ID, fingerprint: fingerprint(row), cancel: cancel, runner: runner}
	delete(r.retryAt, row.ID)
	username, botUserID := runner.Identity()
	if err := r.bots.MarkRunning(ctx, row.ID, username, botUserID); err != nil {
		logger.Warn("Telegram Agent could not record a running bot", logger.Err(err))
	}
	logger.Info("Telegram bot started", logger.Int64("bot_id", row.ID), logger.String("username", username))
}

// commandSpecs lists the command menu of a bot, sorted for a stable hash.
func (r *Runtime) commandSpecs(ctx context.Context, botID int64) ([]telegram.CommandSpec, error) {
	defs, err := r.bots.CommandsFor(ctx, botID)
	if err != nil {
		return nil, err
	}
	specs := make([]telegram.CommandSpec, 0, len(defs))
	for _, def := range defs {
		name := strings.TrimPrefix(strings.TrimSpace(def.Command), "/")
		if name == "" || !strings.HasPrefix(strings.TrimSpace(def.Command), "/") {
			continue
		}
		specs = append(specs, telegram.CommandSpec{Command: name, Description: def.Description})
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Command < specs[j].Command })
	return specs, nil
}

func specsHash(specs []telegram.CommandSpec) string {
	var b strings.Builder
	for _, spec := range specs {
		b.WriteString(spec.Command + "\x00" + spec.Description + "\n")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}

// syncCommands pushes a bot's command menu when it differs from what was last
// pushed. It is what makes a saved command appear in Telegram.
func (r *Runtime) syncCommands(ctx context.Context, botID int64) error {
	bot := r.running[botID]
	if bot == nil {
		return errBotNotRunning
	}
	row, err := r.bots.Get(ctx, botID)
	if err != nil {
		return err
	}
	specs, err := r.commandSpecs(ctx, botID)
	if err != nil {
		return err
	}
	hash := specsHash(specs)
	if row.CommandsHash == hash {
		return nil
	}
	if err := bot.runner.SyncCommands(specs); err != nil {
		logger.Warn("Telegram command sync failed", logger.Int64("bot_id", botID), logger.Err(err))
		return err
	}
	return r.bots.MarkCommandsSynced(ctx, botID, hash)
}

// reconcileAccounts reconnects enabled, authorized accounts the agent does not
// hold a session for and stops the ones that were disabled or logged out.
func (r *Runtime) reconcileAccounts(ctx context.Context) {
	rows, err := r.accounts.ListRunnableAccountsFor(ctx, r.agentID)
	if err != nil {
		logger.Warn("Telegram Agent could not list client accounts", logger.Err(err))
		r.board.Record(JobConnections, err, "")
		return
	}
	var connectErr error
	connected := map[int64]bool{}
	for _, id := range r.clients.ConnectedIDs() {
		connected[id] = true
	}
	runnable := map[int64]bool{}
	for _, row := range rows {
		runnable[row.ID] = true
		if connected[row.ID] || time.Now().Before(r.acctNext[row.ID]) {
			continue
		}
		r.acctNext[row.ID] = time.Now().Add(accountRetryDelay)
		if err := r.clients.StartAccount(row.ID); err != nil {
			logger.Warn("Telegram client account failed to connect", logger.Int64("account_id", row.ID), logger.Err(err))
			_ = r.accounts.SetConnectionResult(ctx, row.ID, false, err.Error())
			connectErr = err
		}
	}
	for id := range connected {
		if !runnable[id] {
			r.clients.StopAccount(id)
		}
	}
	if err := r.accounts.TouchAccounts(ctx, r.clients.ConnectedIDs()); err != nil {
		logger.Warn("Telegram Agent could not refresh account heartbeats", logger.Err(err))
		connectErr = err
	}
	r.board.Record(JobConnections, connectErr, fmt.Sprintf("%d of %d accounts connected", len(r.clients.ConnectedIDs()), len(rows)))
}
