package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/assistant"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func convPath(id any) string {
	switch v := id.(type) {
	case float64:
		return "/assistant/conversations/" + strconv.Itoa(int(v))
	case uint:
		return "/assistant/conversations/" + strconv.Itoa(int(v))
	}
	return "/assistant/conversations/" + id.(string)
}

// startConversation makes an empty conversation for the user and returns its id.
func startConversation(t *testing.T, a *assistantRig, uid int) uint {
	t.Helper()
	rec := a.as(t, uid, http.MethodPost, "/assistant/conversations", map[string]any{})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return uint(decodeData(t, rec.Body.Bytes())["id"].(float64))
}

// askIn asks a question inside a conversation.
func askIn(t *testing.T, a *assistantRig, uid int, id uint, text string) *httptest.ResponseRecorder {
	t.Helper()
	return a.as(t, uid, http.MethodPost, "/assistant/chat", map[string]any{"messages": []assistant.Turn{user(text)}, "lang": "en", "conversation_id": id})
}

func TestAConversationKeepsTheQuestionsAndAnswersAndGivesThemBackToTheModel(t *testing.T) {
	a := newAssistantRig(t, true)
	id := startConversation(t, a, 1)

	a.llm.steps = []step{ask(assistant.ToolCall{ID: "c1", Name: "list_modules", Arguments: `{}`}), say("There is one module, shop.")}
	rec := askIn(t, a, 1, id, "which modules are there?")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// the second question: the model is given the first two messages from the database, not from the page
	a.llm.steps = []step{func(messages []assistant.Message, _ []assistant.ToolSpec) (*assistant.Completion, error) {
		var roles, texts []string
		for _, m := range messages[1:] {
			roles, texts = append(roles, m.Role), append(texts, m.Content)
		}
		assert.Equal(t, []string{"user", "assistant", "user"}, roles)
		assert.Equal(t, "which modules are there?", texts[0])
		assert.Equal(t, "There is one module, shop.", texts[1])
		return &assistant.Completion{Content: "It is enabled."}, nil
	}}
	require.Equal(t, http.StatusOK, askIn(t, a, 1, id, "is it enabled?").Code)

	rec = a.as(t, 1, http.MethodGet, convPath(id), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := decodeData(t, rec.Body.Bytes())
	conv := data["conversation"].(map[string]any)
	assert.Equal(t, "which modules are there?", conv["title"], "the first question is the title until it is renamed")
	assert.EqualValues(t, 4, conv["message_count"])
	msgs := data["messages"].([]any)
	require.Len(t, msgs, 4)
	assert.Equal(t, "user", msgs[0].(map[string]any)["role"])
	first := msgs[1].(map[string]any)
	assert.Equal(t, "There is one module, shop.", first["content"])
	tools := first["tools"].([]any)
	require.Len(t, tools, 1)
	assert.Equal(t, "list_modules", tools[0].(map[string]any)["name"], "a restored answer still shows where it came from")
	assert.Equal(t, "is it enabled?", msgs[2].(map[string]any)["content"])

	// a question without a conversation is asked as before and keeps nothing
	a.llm.steps = []step{say("ok")}
	require.Equal(t, http.StatusOK, a.as(t, 1, http.MethodPost, "/assistant/chat", map[string]any{"messages": []assistant.Turn{user("hi")}, "lang": "en"}).Code)
	var n int64
	require.NoError(t, model.DB.Model(&model.AssistantMessage{}).Count(&n).Error)
	assert.EqualValues(t, 4, n)
}

func TestOnlyTheOwnerSeesChangesAndDeletesAConversation(t *testing.T) {
	a := newAssistantRig(t, true)
	mine := startConversation(t, a, 1)
	a.llm.steps = []step{say("secret answer")}
	require.Equal(t, http.StatusOK, askIn(t, a, 1, mine, "a private question").Code)

	// another user: it does not exist, for every call, and nothing leaks into the list
	assert.Equal(t, http.StatusNotFound, a.as(t, 2, http.MethodGet, convPath(mine), nil).Code)
	assert.Equal(t, http.StatusNotFound, a.as(t, 2, http.MethodPut, convPath(mine), map[string]any{"title": "mine now"}).Code)
	assert.Equal(t, http.StatusNotFound, a.as(t, 2, http.MethodDelete, convPath(mine), nil).Code)
	a.llm.steps = []step{say("never")}
	rec := askIn(t, a, 2, mine, "can I join?")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Len(t, a.llm.steps, 1, "the model is not asked")
	list := a.as(t, 2, http.MethodGet, "/assistant/conversations", nil)
	require.Equal(t, http.StatusOK, list.Code)
	assert.NotContains(t, list.Body.String(), "private")
	assert.EqualValues(t, 0, decodeData(t, list.Body.Bytes())["total"])
	batch := a.as(t, 2, http.MethodPost, "/assistant/conversations/batch-delete", map[string]any{"ids": []uint{mine}})
	require.Equal(t, http.StatusOK, batch.Code)
	assert.EqualValues(t, 0, decodeData(t, batch.Body.Bytes())["deleted"])

	// still whole for the owner
	rec = a.as(t, 1, http.MethodGet, convPath(mine), nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "secret answer")

	// no user (a developer credential): no conversations at all
	assert.Equal(t, http.StatusBadRequest, a.as(t, 0, http.MethodPost, "/assistant/conversations", map[string]any{}).Code)
	assert.Equal(t, http.StatusBadRequest, a.as(t, 0, http.MethodGet, "/assistant/conversations", nil).Code)
}

func TestAConversationIsRenamedAndDeletedWithItsMessages(t *testing.T) {
	a := newAssistantRig(t, true)
	id := startConversation(t, a, 1)
	a.llm.steps = []step{say("ok")}
	require.Equal(t, http.StatusOK, askIn(t, a, 1, id, "line one\nline two "+strings.Repeat("x", 200)).Code)

	rec := a.as(t, 1, http.MethodGet, convPath(id), nil)
	title := decodeData(t, rec.Body.Bytes())["conversation"].(map[string]any)["title"].(string)
	assert.NotContains(t, title, "\n")
	assert.LessOrEqual(t, len([]rune(title)), assistant.MaxTitleRunes)

	assert.Equal(t, http.StatusBadRequest, a.as(t, 1, http.MethodPut, convPath(id), map[string]any{"title": "   "}).Code)
	require.Equal(t, http.StatusOK, a.as(t, 1, http.MethodPut, convPath(id), map[string]any{"title": "Capacity review"}).Code)
	list := decodeData(t, a.as(t, 1, http.MethodGet, "/assistant/conversations?keyword=capacity", nil).Body.Bytes())
	assert.EqualValues(t, 1, list["total"])
	assert.EqualValues(t, 0, decodeData(t, a.as(t, 1, http.MethodGet, "/assistant/conversations?keyword=nothing", nil).Body.Bytes())["total"])

	require.Equal(t, http.StatusOK, a.as(t, 1, http.MethodDelete, convPath(id), nil).Code)
	assert.Equal(t, http.StatusNotFound, a.as(t, 1, http.MethodGet, convPath(id), nil).Code)
	var n int64
	require.NoError(t, model.DB.Model(&model.AssistantMessage{}).Count(&n).Error)
	assert.Zero(t, n, "the messages go with it")
}

func TestAFailedQuestionIsKeptAsAFailureAndIsNotGivenBackToTheModel(t *testing.T) {
	a := newAssistantRig(t, true)
	id := startConversation(t, a, 1)
	a.llm.steps = nil // the model cannot be reached
	rec := askIn(t, a, 1, id, "how is the service?")
	require.NotEqual(t, http.StatusOK, rec.Code)

	a.llm.steps = []step{func(messages []assistant.Message, _ []assistant.ToolSpec) (*assistant.Completion, error) {
		for _, m := range messages[1:] {
			assert.NotContains(t, m.Content, "the script has no more answers", "the failure text is not part of what was said")
		}
		return &assistant.Completion{Content: "ok"}, nil
	}}
	require.Equal(t, http.StatusOK, askIn(t, a, 1, id, "and now?").Code)

	msgs := decodeData(t, a.as(t, 1, http.MethodGet, convPath(id), nil).Body.Bytes())["messages"].([]any)
	require.Len(t, msgs, 4)
	assert.Equal(t, true, msgs[1].(map[string]any)["failed"])
	assert.Equal(t, false, msgs[3].(map[string]any)["failed"])

	// a question that was never asked (not a valid question) leaves no trace
	before := len(msgs)
	bad := a.as(t, 1, http.MethodPost, "/assistant/chat", map[string]any{"messages": []assistant.Turn{user("   ")}, "lang": "en", "conversation_id": id})
	assert.Equal(t, http.StatusBadRequest, bad.Code)
	msgs = decodeData(t, a.as(t, 1, http.MethodGet, convPath(id), nil).Body.Bytes())["messages"].([]any)
	assert.Len(t, msgs, before)
}

func TestACommandOfTheChatIsKeptInTheConversationToo(t *testing.T) {
	a := newAssistantRig(t, true)
	id := startConversation(t, a, 1)
	rec := a.as(t, 1, http.MethodPost, "/assistant/skill", map[string]any{"name": "report", "args": "", "lang": "en", "conversation_id": id})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	msgs := decodeData(t, a.as(t, 1, http.MethodGet, convPath(id), nil).Body.Bytes())["messages"].([]any)
	require.Len(t, msgs, 2)
	assert.Equal(t, "/report", msgs[0].(map[string]any)["content"])
	assert.Equal(t, "assistant", msgs[1].(map[string]any)["role"])
	assert.Equal(t, http.StatusNotFound, a.as(t, 2, http.MethodPost, "/assistant/skill", map[string]any{"name": "report", "lang": "en", "conversation_id": id}).Code)
}

func TestAUserKeepsAtMostTwoHundredConversationsAndAConversationGrowsToALimit(t *testing.T) {
	a := newAssistantRig(t, true)
	now := time.Now()
	rows := make([]model.AssistantConversation, 0, assistant.MaxConversations)
	for i := 0; i < assistant.MaxConversations; i++ {
		rows = append(rows, model.AssistantConversation{OwnerID: 1, Title: "t", CreatedAt: now, UpdatedAt: now})
	}
	require.NoError(t, model.DB.CreateInBatches(&rows, 100).Error)
	assert.Equal(t, http.StatusBadRequest, a.as(t, 1, http.MethodPost, "/assistant/conversations", map[string]any{}).Code)
	assert.Equal(t, http.StatusOK, a.as(t, 2, http.MethodPost, "/assistant/conversations", map[string]any{}).Code, "the limit is each user's own")

	full := startConversation(t, a, 2)
	msgs := make([]model.AssistantMessage, 0, assistant.MaxStoredMessages)
	for i := 0; i < assistant.MaxStoredMessages; i++ {
		msgs = append(msgs, model.AssistantMessage{ConversationID: full, Role: "user", Content: "q", CreatedAt: now})
	}
	require.NoError(t, model.DB.CreateInBatches(&msgs, 100).Error)
	a.llm.steps = []step{say("never")}
	rec := askIn(t, a, 2, full, "one more")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "full")
	assert.Len(t, a.llm.steps, 1, "the model is not asked")
}

func TestConversationsQuietForLongerThanTheRetentionAreDeletedWithTheirMessages(t *testing.T) {
	a := newAssistantRig(t, true)
	ctx := context.Background()
	require.NoError(t, model.DB.Create(&model.SysConfig{ConfigKey: timing.ConversationRetentionDays.Key, ConfigValue: "30"}).Error)
	now := runtimeNow
	mk := func(age time.Duration) uint {
		c := model.AssistantConversation{OwnerID: 1, Title: "t", CreatedAt: now.Add(-age), UpdatedAt: now.Add(-age)}
		require.NoError(t, model.DB.Create(&c).Error)
		require.NoError(t, model.DB.Create(&model.AssistantMessage{ConversationID: c.ID, Role: "user", Content: "q", CreatedAt: now.Add(-age)}).Error)
		return c.ID
	}
	old1, old2 := mk(31*24*time.Hour), mk(400*24*time.Hour)
	fresh := mk(29 * 24 * time.Hour)

	n, err := a.svc.PruneConversations(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
	var ids []uint
	require.NoError(t, model.DB.Model(&model.AssistantConversation{}).Pluck("id", &ids).Error)
	assert.Equal(t, []uint{fresh}, ids)
	assert.NotContains(t, ids, old1)
	assert.NotContains(t, ids, old2)
	var left int64
	require.NoError(t, model.DB.Model(&model.AssistantMessage{}).Count(&left).Error)
	assert.EqualValues(t, 1, left, "only the fresh conversation keeps its message")

	// without a stored value the default holds; a value out of range is brought into it
	require.NoError(t, model.DB.Where("config_key = ?", timing.ConversationRetentionDays.Key).Delete(&model.SysConfig{}).Error)
	assert.Equal(t, 90, timing.ConversationRetentionDays.Parse(""))
	assert.Equal(t, 1, timing.ConversationRetentionDays.Parse("0"))
	assert.Equal(t, 3650, timing.ConversationRetentionDays.Parse("99999"))
}
