package api

import (
	"context"
	"errors"
	"fmt"
	"github.com/coder/websocket"
	"github.com/cylonchau/kube-resource-fcst/pkg/app/query"
	"github.com/cylonchau/kube-resource-fcst/pkg/logger"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"strings"
	"sync"
	"time"
)

const (
	statsStreamPollInterval = 2 * time.Second
	// statsFingerprintTTL lets every connection of one process share a single database
	// look per topic, however many pages are open.
	statsFingerprintTTL = 1500 * time.Millisecond
)

// statsTopics maps a topic to the tables whose change means its statistics changed. A page
// subscribes to the topics it shows numbers for and re-reads its own (filtered) statistics
// when one fires; the push carries no numbers.
var statsTopics = map[string][]string{
	"tasks": {"job", "task_definition"},
}
var appendOnlyTables = map[string]bool{"job": true}

// StatsRouter serves the statistics stream.
type StatsRouter struct {
	DB *gorm.DB

	mu    sync.Mutex
	cache map[string]cachedFingerprint
}

// fingerprintVariant remembers, per table, which variant of the fingerprint query it answers.
var fingerprintVariant sync.Map

type cachedFingerprint struct {
	value string
	at    time.Time
}

// tableFingerprint says "did anything in this table change": the newest update, the newest
// soft delete, the highest id and the row count. Tables without those columns fall back to
// what they have.
func tableFingerprint(ctx context.Context, db *gorm.DB, table string) string {
	variants := []string{
		"SELECT COALESCE(MAX(updated_at),''), COALESCE(MAX(deleted_at),''), COALESCE(MAX(id),0), COUNT(*) FROM %s",
		"SELECT COALESCE(MAX(updated_at),''), '', COALESCE(MAX(id),0), COUNT(*) FROM %s",
		"SELECT COALESCE(MAX(updated_at),''), '', 0, COUNT(*) FROM %s",
		"SELECT '', '', COALESCE(MAX(id),0), COUNT(*) FROM %s",
		"SELECT '', '', 0, COUNT(*) FROM %s",
	}
	if appendOnlyTables[table] {
		variants = []string{
			"SELECT COALESCE(MAX(updated_at),''), '', COALESCE(MAX(id),0), 0 FROM %s",
			"SELECT '', '', COALESCE(MAX(id),0), 0 FROM %s",
		}
	}
	// A table answers the same variant every time, so the one that worked is tried first from then on: a variant that
	// fails is still a statement on the (single, with SQLite) connection, and a table without a deleted_at column
	// would otherwise cost two failed statements before the third one answers, at every look.
	first := 0
	if known, ok := fingerprintVariant.Load(table); ok {
		first = known.(int)
	}
	for n := 0; n < len(variants); n++ {
		i := (first + n) % len(variants)
		var updated, deleted, id, count interface{}
		row := db.WithContext(ctx).Raw(fmt.Sprintf(variants[i], table)).Row()
		if err := row.Scan(&updated, &deleted, &id, &count); err == nil {
			fingerprintVariant.Store(table, i)
			return fmt.Sprintf("%s|%v|%v|%v|%v", table, updated, deleted, id, count)
		}
	}
	return table + "|unavailable"
}

// fingerprint is the combined state of a topic, cached briefly.
func (router *StatsRouter) fingerprint(ctx context.Context, topic string) string {
	router.mu.Lock()
	if cached, ok := router.cache[topic]; ok && time.Since(cached.at) < statsFingerprintTTL {
		router.mu.Unlock()
		return cached.value
	}
	router.mu.Unlock()

	parts := make([]string, 0, len(statsTopics[topic]))
	for _, table := range statsTopics[topic] {
		parts = append(parts, tableFingerprint(ctx, router.DB, table))
	}
	value := strings.Join(parts, ";")

	router.mu.Lock()
	if router.cache == nil {
		router.cache = map[string]cachedFingerprint{}
	}
	router.cache[topic] = cachedFingerprint{value: value, at: time.Now()}
	router.mu.Unlock()
	return value
}

// parseStatsTopics keeps the known topics of a comma separated list, once each.
func parseStatsTopics(raw string) []string {
	seen := map[string]bool{}
	topics := []string{}
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if _, known := statsTopics[name]; known && !seen[name] {
			seen[name] = true
			topics = append(topics, name)
		}
	}
	return topics
}

// StreamStats tells a connected page the moment the data behind its statistics changes, so
// the numbers refresh by themselves instead of by polling or a manual reload.
func (router *StatsRouter) StreamStats(c *gin.Context) {
	if router.DB == nil {
		query.InternalError(c, errors.New("database is unavailable"))
		return
	}
	topics := parseStatsTopics(c.Query("topics"))
	if len(topics) == 0 {
		query.BadRequest(c, errors.New("topics must name at least one known statistics topic"))
		return
	}
	// The baseline is taken before the handshake so a change right after the client
	// connects is pushed, never swallowed.
	last := make(map[string]string, len(topics))
	for _, topic := range topics {
		last[topic] = router.fingerprint(c.Request.Context(), topic)
	}
	conn, err := websocket.Accept(newWebsocketWriter(c.Writer), c.Request, statsTransportAcceptOptions())
	if err != nil {
		logger.Warn("stats stream handshake failed", logger.Err(err))
		return
	}
	defer conn.CloseNow()
	ctx := conn.CloseRead(c.Request.Context())

	poll := time.NewTicker(statsStreamPollInterval)
	defer poll.Stop()
	ping := time.NewTicker(statsTransportPingInterval)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			pingCtx, cancel := context.WithTimeout(ctx, statsTransportWriteTimeout)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				return
			}
		case <-poll.C:
			for _, topic := range topics {
				now := router.fingerprint(ctx, topic)
				if now == last[topic] {
					continue
				}
				last[topic] = now
				if !writeStream(ctx, conn, streamMessage{Type: "stats", Data: map[string]string{"topic": topic}}) {
					return
				}
			}
		}
	}
}
