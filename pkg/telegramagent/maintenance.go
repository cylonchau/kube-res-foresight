package telegramagent

import (
	"context"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/logger"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"os"
	"strconv"
	"time"
)

const (
	// consumedGrace is how long a consumed message stays visible on the
	// platform's Messages page before the cleanup job deletes it.
	consumedGrace = 5 * time.Minute
	// defaultCacheTTL is how long a message may sit in the cache, consumed or
	// not, before it is deleted; ConfigCacheTTLHours overrides it.
	defaultCacheTTL = 24 * time.Hour
)

// cacheTTL reads the cache retention from the settings.
func (r *Runtime) cacheTTL(ctx context.Context) time.Duration {
	if r.config == nil {
		return defaultCacheTTL
	}
	raw, err := r.config.GetValueOrDefault(ctx, ConfigCacheTTLHours, "")
	if err != nil || raw == "" {
		return defaultCacheTTL
	}
	hours, err := strconv.Atoi(raw)
	if err != nil || hours <= 0 {
		return defaultCacheTTL
	}
	return time.Duration(hours) * time.Hour
}

// Cleanup is the cache maintenance job: it deletes consumed messages after a
// short grace period and anything older than the cache TTL, so no history
// builds up.
func (r *Runtime) Cleanup(ctx context.Context, now time.Time) {
	consumed, expired, err := r.messageDAO.CleanMessages(ctx, now.Add(-consumedGrace), now.Add(-r.cacheTTL(ctx)))
	if err != nil {
		logger.Warn("Telegram cache cleanup failed", logger.Err(err))
	}
	r.board.Record(JobCleanup, err, fmt.Sprintf("deleted %d consumed, %d expired", consumed, expired))
}

// Report writes the agent's heartbeat: what it runs, how the cache looks and
// how its jobs did. The platform's Agent status page reads it.
func (r *Runtime) Report(ctx context.Context) {
	stats, err := r.messageDAO.MessageStats(ctx)
	if err != nil {
		logger.Warn("Telegram agent could not count the cache", logger.Err(err))
	}
	status := &model.TelegramAgentStatus{
		AgentID:   r.agentID,
		Version:   AgentVersion,
		StartedAt: r.startedAt,
		Accounts:  uint16(len(r.clients.ConnectedIDs())),
		Bots:      uint16(len(r.running)),
		Pending:   uint32(stats[model.TelegramMsgPending]),
		Forwarded: r.forward.Forwarded(),
		Failed:    r.forward.Failed(),
		LastError: r.board.LastError(),
		JobsJSON:  r.board.JSON(),
	}
	if err := r.statusDAO.Report(ctx, status); err != nil {
		logger.Warn("Telegram agent could not report its status", logger.Err(err))
	}
}

// DefaultAgentID names this agent after its host unless one is configured.
func DefaultAgentID() string {
	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}
	return "telegram-agent"
}
