package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	"gorm.io/gorm"
)

// Conversations are kept for the user who held them, so a conversation survives a closed browser and can be picked up from the window or from
// a page. Only the owner reads one: every call here is bound to the user in the context, and a question with no user (a developer credential, a
// scheduled run) has no conversation. The platform stores what the user saw, not what the model was given: the page context, the tool results
// and the rules are made again for every question.

const (
	// MaxConversations is how many conversations one user keeps; past it the user deletes some (the clean-up also deletes old ones).
	MaxConversations = 200
	// MaxStoredMessages is how long one conversation grows; past it the user starts a new one.
	MaxStoredMessages = 400
	// MaxTitleRunes is the length of a title, the first question cut short until the user renames the conversation.
	MaxTitleRunes = 60
	// pruneBatch is how many conversations one delete of the clean-up takes.
	pruneBatch = 200
)

// ErrNoConversation is a conversation that is not there, or is another user's: the two are not told apart.
var ErrNoConversation = RequestError{Msg: "the conversation does not exist"}

func (s *Service) conversationOwner(ctx context.Context) (Owner, error) {
	o, ok := ownerOf(ctx)
	if !ok || s.Conversations == nil {
		return Owner{}, RequestError{Msg: "conversations are kept for a signed-in user only"}
	}
	return o, nil
}

// TitleOf makes a title from a question: one line, cut short.
func TitleOf(text string) string {
	line := strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(line) > MaxTitleRunes {
		line = string([]rune(line)[:MaxTitleRunes])
	}
	return line
}

// StartConversation begins an empty conversation of the user; its title is the first question once there is one.
func (s *Service) StartConversation(ctx context.Context, title string) (*model.AssistantConversation, error) {
	o, err := s.conversationOwner(ctx)
	if err != nil {
		return nil, err
	}
	n, err := s.Conversations.Count(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	if n >= MaxConversations {
		return nil, RequestError{Msg: "there are already 200 conversations: delete some before you start a new one"}
	}
	now := s.now().UTC()
	c := &model.AssistantConversation{OwnerID: o.ID, Title: TitleOf(title), CreatedAt: now, UpdatedAt: now}
	return c, s.Conversations.Create(ctx, c)
}

// notFound turns a missing row into the error of a conversation that is not there.
func notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNoConversation
	}
	return err
}

// Conversation returns a conversation of the user with its last messages.
func (s *Service) Conversation(ctx context.Context, id uint) (*model.AssistantConversation, []*model.AssistantMessage, error) {
	o, err := s.conversationOwner(ctx)
	if err != nil {
		return nil, nil, err
	}
	c, err := s.Conversations.Get(ctx, o.ID, id)
	if err != nil {
		return nil, nil, notFound(err)
	}
	msgs, err := s.Conversations.Messages(ctx, o.ID, id, MaxStoredMessages)
	return c, msgs, err
}

// ListConversations lists the conversations of the user, the latest first.
func (s *Service) ListConversations(ctx context.Context, keyword string, page, pageSize int) ([]*model.AssistantConversation, int64, error) {
	o, err := s.conversationOwner(ctx)
	if err != nil {
		return nil, 0, err
	}
	return s.Conversations.List(ctx, o.ID, keyword, page, pageSize)
}

// RenameConversation sets the title of a conversation of the user.
func (s *Service) RenameConversation(ctx context.Context, id uint, title string) error {
	o, err := s.conversationOwner(ctx)
	if err != nil {
		return err
	}
	title = TitleOf(title)
	if title == "" {
		return RequestError{Msg: "the title is empty"}
	}
	return notFound(s.Conversations.Rename(ctx, o.ID, id, title))
}

// DeleteConversations deletes conversations of the user with their messages; it returns how many went.
func (s *Service) DeleteConversations(ctx context.Context, ids []uint) (int64, error) {
	o, err := s.conversationOwner(ctx)
	if err != nil {
		return 0, err
	}
	return s.Conversations.Delete(ctx, o.ID, ids)
}

// storedHistory is what the model is given of a conversation: its messages that were answers or questions, the newest maxHistory-1 of them.
// A failure text was shown to the user and is not part of what was said.
func (s *Service) storedHistory(ctx context.Context, o Owner, id uint) ([]Turn, int, error) {
	if _, err := s.Conversations.Get(ctx, o.ID, id); err != nil {
		return nil, 0, notFound(err)
	}
	msgs, err := s.Conversations.Messages(ctx, o.ID, id, MaxStoredMessages)
	if err != nil {
		return nil, 0, err
	}
	turns := make([]Turn, 0, len(msgs))
	for _, m := range msgs {
		if m.Failed {
			continue
		}
		content := m.Content
		if utf8.RuneCountInString(content) > maxMessageRunes {
			content = string([]rune(content)[:maxMessageRunes])
		}
		turns = append(turns, Turn{Role: m.Role, Content: content})
	}
	if len(turns) > maxHistory-1 {
		turns = turns[len(turns)-(maxHistory-1):]
	}
	return turns, len(msgs), nil
}

// record keeps a question and what came of it. A question that was not asked at all (the assistant is off, the request is not valid, the
// service is busy) leaves no trace.
func (s *Service) record(ctx context.Context, o Owner, id uint, question string, reply *Reply, askErr error) error {
	var notAsked ErrNotReady
	if askErr != nil && (errors.As(askErr, &notAsked) || IsRequestError(askErr) || errors.Is(askErr, ErrBusy)) {
		return nil
	}
	asked := &model.AssistantMessage{Role: "user", Content: question}
	answer := &model.AssistantMessage{Role: "assistant"}
	if askErr != nil {
		answer.Content, answer.Failed = clip(askErr.Error(), 500), true
	} else {
		answer.Content = reply.Content
		if b, err := json.Marshal(reply.Tools); err == nil && len(reply.Tools) > 0 {
			answer.Tools = string(b)
		}
		if b, err := json.Marshal(reply.Attachments); err == nil && len(reply.Attachments) > 0 {
			answer.Attachments = string(b)
		}
	}
	// the clock of the question, not of the answer, so the messages keep their order; the answer is a moment later
	now := s.now().UTC()
	return s.Conversations.Append(ctx, o.ID, id, now, TitleOf(question), asked, answer)
}

// ChatConversation answers a question inside a stored conversation of the user: the model is given the conversation so far, and the question and the answer
// are kept in it.
func (s *Service) ChatConversation(ctx context.Context, id uint, question, lang string, page *PageContext) (*Reply, error) {
	o, err := s.conversationOwner(ctx)
	if err != nil {
		return nil, err
	}
	history, stored, err := s.storedHistory(ctx, o, id)
	if err != nil {
		return nil, err
	}
	if stored+2 > MaxStoredMessages {
		return nil, RequestError{Msg: "this conversation is full: start a new one"}
	}
	ctx = withConversation(ctx, id)
	reply, askErr := s.ChatIn(ctx, append(history, Turn{Role: "user", Content: question}), lang, page)
	if err := s.record(ctx, o, id, question, reply, askErr); err != nil && askErr == nil {
		return nil, err
	}
	return reply, askErr
}

// SkillConversation runs a skill of the chat inside a stored conversation: the command and its answer are kept in it, and so the next question
// has them in the history, as in the window.
func (s *Service) SkillConversation(ctx context.Context, id uint, name, args, lang string) (*Reply, error) {
	o, err := s.conversationOwner(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.Conversations.Get(ctx, o.ID, id); err != nil {
		return nil, notFound(err)
	}
	ctx = withConversation(ctx, id)
	reply, askErr := s.RunSkill(ctx, name, args, lang)
	command := strings.TrimSpace("/" + name + " " + args)
	if err := s.record(ctx, o, id, command, reply, askErr); err != nil && askErr == nil {
		return nil, err
	}
	return reply, askErr
}

// PruneConversations deletes the conversations that have been quiet for longer than the retention parameter, in batches, and returns how many
// went. It stops when the context ends; what is left is deleted by the next run.
func (s *Service) PruneConversations(ctx context.Context) (int64, error) {
	if s.Conversations == nil {
		return 0, nil
	}
	days := timing.ConversationRetentionDays.Default
	if s.Config != nil {
		if raw, err := s.Config.GetValueOrDefault(ctx, timing.ConversationRetentionDays.Key, ""); err == nil {
			days = timing.ConversationRetentionDays.Parse(raw)
		}
	}
	cutoff := s.now().Add(-time.Duration(days) * 24 * time.Hour)
	var total int64
	for ctx.Err() == nil {
		n, err := s.Conversations.PruneBefore(ctx, cutoff, pruneBatch)
		total += n
		if err != nil || n < pruneBatch {
			return total, err
		}
	}
	return total, ctx.Err()
}
