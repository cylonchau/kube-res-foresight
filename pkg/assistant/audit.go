package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
)

// Every question put to a language model leaves an audit record: who asked, from where, which model answered in which mode, which tools were
// read and which were refused, how long it took and the tokens it used. The record holds no text of the question, of the answer or of a tool
// result, so an administrator who reads the audit sees how the assistant is used and never what a user said. A question that was never asked
// (the assistant is off, the request is not valid, the service is busy) is not recorded: nothing was sent anywhere.

type sourceKey struct{}

// withSource says where the question comes from (chat, skill or task) for the audit; a question with no source is a chat.
func withSource(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, sourceKey{}, source)
}

func sourceOf(ctx context.Context) string {
	if s, ok := ctx.Value(sourceKey{}).(string); ok && s != "" {
		return s
	}
	return model.AIAuditChat
}

type trailKey struct{}

// trail collects what one question did, for its audit record.
type trail struct {
	mu      sync.Mutex
	calls   int
	prompt  int
	reply   int
	refused []string
	mode    ToolMode
}

func (t *trail) refuse(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, n := range t.refused {
		if n == name {
			return
		}
	}
	t.refused = append(t.refused, name)
}

func trailOf(ctx context.Context) *trail {
	t, _ := ctx.Value(trailKey{}).(*trail)
	return t
}

// metered counts what a question asks of the model.
type metered struct {
	inner LLM
	trail *trail
}

func (m metered) Complete(ctx context.Context, messages []Message, tools []ToolSpec, maxTokens int) (*Completion, error) {
	done, err := m.inner.Complete(ctx, messages, tools, maxTokens)
	m.trail.mu.Lock()
	m.trail.calls++
	if done != nil {
		m.trail.prompt += done.PromptTokens
		m.trail.reply += done.CompletionTokens
	}
	m.trail.mu.Unlock()
	return done, err
}

// notRecorded is an error of a question that was not asked at all.
func notRecorded(err error) bool {
	var notReady ErrNotReady
	return errors.As(err, &notReady) || IsRequestError(err) || errors.Is(err, ErrBusy)
}

// audit writes the record of a question. A failure to write is logged by the caller's logger-free path: the answer is not held back for it.
func (s *Service) audit(ctx context.Context, conn Connection, page *PageContext, conversation uint, started time.Time, t *trail, reply *Reply, askErr error) {
	if s.Audits == nil {
		return
	}
	rec := &model.AssistantAudit{CreatedAt: started.UTC(), Source: sourceOf(ctx), ConversationID: conversation, ConnectionID: conn.ID, Model: conn.Model,
		Status: "success", DurationMS: s.now().Sub(started).Milliseconds()}
	if o, ok := ownerOf(ctx); ok {
		rec.UserID, rec.UserName = o.ID, o.Name
	}
	if page != nil {
		rec.Page = page.Page
	}
	if askErr != nil {
		rec.Status, rec.Error = "failed", clip(askErr.Error(), 480)
	}
	t.mu.Lock()
	rec.LLMCalls, rec.PromptTokens, rec.CompletionTokens = t.calls, t.prompt, t.reply
	refused := append([]string{}, t.refused...)
	rec.ToolMode = string(t.mode)
	t.mu.Unlock()
	type used struct {
		Name string `json:"name"`
		OK   bool   `json:"ok"`
	}
	tools := []used{}
	if reply != nil {
		for _, tr := range reply.Tools {
			tools = append(tools, used{Name: tr.Name, OK: tr.OK})
		}
	}
	if b, err := json.Marshal(tools); err == nil {
		rec.Tools = string(b)
	}
	if b, err := json.Marshal(refused); err == nil {
		rec.Refused = string(b)
	}
	_ = s.Audits.Create(ctx, rec)
}

// PruneAudits deletes the audit records older than the retention parameter, in batches, and returns how many went.
func (s *Service) PruneAudits(ctx context.Context) (int64, error) {
	if s.Audits == nil {
		return 0, nil
	}
	days := timing.AuditRetentionDays.Default
	if s.Config != nil {
		if raw, err := s.Config.GetValueOrDefault(ctx, timing.AuditRetentionDays.Key, ""); err == nil {
			days = timing.AuditRetentionDays.Parse(raw)
		}
	}
	cutoff := s.now().Add(-time.Duration(days) * 24 * time.Hour)
	var total int64
	for ctx.Err() == nil {
		n, err := s.Audits.PruneBefore(ctx, cutoff, pruneBatch)
		total += n
		if err != nil || n < pruneBatch {
			return total, err
		}
	}
	return total, ctx.Err()
}

type conversationKey struct{}

// withConversation says which stored conversation a question is asked in, for the audit.
func withConversation(ctx context.Context, id uint) context.Context {
	return context.WithValue(ctx, conversationKey{}, id)
}

func conversationOf(ctx context.Context) uint {
	id, _ := ctx.Value(conversationKey{}).(uint)
	return id
}

type progressKey struct{}

// WithProgress gives a question a function that is told what it does as it goes (a tool was read): an async task shows it to its owner while the
// answer is still being made. The function is called from the goroutine of the question and must not block.
func WithProgress(ctx context.Context, report func(kind string, data any)) context.Context {
	return context.WithValue(ctx, progressKey{}, report)
}

func reportProgress(ctx context.Context, kind string, data any) {
	if report, ok := ctx.Value(progressKey{}).(func(string, any)); ok && report != nil {
		report(kind, data)
	}
}

// PruneAsync deletes the finished background tasks older than the retention parameter, in batches, and returns how many went.
func (s *Service) PruneAsync(ctx context.Context) (int64, error) {
	if s.AsyncTasks == nil {
		return 0, nil
	}
	hours := timing.AsyncRetentionHours.Default
	if s.Config != nil {
		if raw, err := s.Config.GetValueOrDefault(ctx, timing.AsyncRetentionHours.Key, ""); err == nil {
			hours = timing.AsyncRetentionHours.Parse(raw)
		}
	}
	cutoff := s.now().UTC().Add(-time.Duration(hours) * time.Hour)
	var total int64
	for ctx.Err() == nil {
		n, err := s.AsyncTasks.PruneBefore(ctx, cutoff, pruneBatch)
		total += n
		if err != nil || n < pruneBatch {
			return total, err
		}
	}
	return total, ctx.Err()
}
