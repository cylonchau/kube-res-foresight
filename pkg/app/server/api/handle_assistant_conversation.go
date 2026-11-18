package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/assistant"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/gin-gonic/gin"
)

// The stored conversations of the assistant. Each one belongs to the user who held it and is read, renamed and deleted by that user only: the
// requests carry no owner, the signed-in user is taken from the session, and another user's id is a conversation that does not exist.

// AssistantConversationDTO is a conversation as the page lists it.
type AssistantConversationDTO struct {
	ID           uint      `json:"id"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Title        string    `json:"title"`
	MessageCount int       `json:"message_count"`
}

func conversationDTO(c *model.AssistantConversation) AssistantConversationDTO {
	return AssistantConversationDTO{ID: c.ID, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, Title: c.Title, MessageCount: c.MessageCount}
}

// AssistantStoredMessage is one message of a conversation as the page shows it.
type AssistantStoredMessage struct {
	ID          uint                   `json:"id"`
	CreatedAt   time.Time              `json:"created_at"`
	Role        string                 `json:"role"`
	Content     string                 `json:"content"`
	Failed      bool                   `json:"failed"`
	Tools       []assistant.ToolTrace  `json:"tools"`
	Attachments []assistant.Attachment `json:"attachments"`
}

func storedMessages(rows []*model.AssistantMessage) []AssistantStoredMessage {
	out := make([]AssistantStoredMessage, 0, len(rows))
	for _, m := range rows {
		item := AssistantStoredMessage{ID: m.ID, CreatedAt: m.CreatedAt, Role: m.Role, Content: m.Content, Failed: m.Failed,
			Tools: []assistant.ToolTrace{}, Attachments: []assistant.Attachment{}}
		if m.Tools != "" {
			_ = json.Unmarshal([]byte(m.Tools), &item.Tools)
		}
		if m.Attachments != "" {
			_ = json.Unmarshal([]byte(m.Attachments), &item.Attachments)
		}
		out = append(out, item)
	}
	return out
}

// conversationFailure answers a failed call on a conversation: the user's own mistake is a 400, a conversation that is not there a 404.
func conversationFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, assistant.ErrNoConversation):
		query.ErrorResponse(c, http.StatusNotFound, err)
	case assistant.IsRequestError(err):
		query.BadRequest(c, err)
	default:
		query.InternalError(c, err)
	}
}

// ListConversations serves GET /assistant/conversations?keyword=&page=&page_size=.
func (r *AssistantRouter) ListConversations(c *gin.Context) {
	page, size := pageParams(c)
	list, total, err := r.Service.ListConversations(withOwner(c), c.Query("keyword"), page, size)
	if err != nil {
		conversationFailure(c, err)
		return
	}
	out := make([]AssistantConversationDTO, 0, len(list))
	for _, row := range list {
		out = append(out, conversationDTO(row))
	}
	query.SuccessResponse(c, nil, gin.H{"list": out, "total": total, "page": page, "pageSize": size})
}

// CreateConversation serves POST /assistant/conversations: an empty conversation, to ask the questions in.
func (r *AssistantRouter) CreateConversation(c *gin.Context) {
	var req struct {
		Title string `json:"title"`
	}
	_ = c.ShouldBindJSON(&req) // the title is optional
	row, err := r.Service.StartConversation(withOwner(c), req.Title)
	if err != nil {
		conversationFailure(c, err)
		return
	}
	query.SuccessResponse(c, nil, conversationDTO(row))
}

// GetConversation serves GET /assistant/conversations/:id: the conversation with its messages.
func (r *AssistantRouter) GetConversation(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	row, msgs, err := r.Service.Conversation(withOwner(c), id)
	if err != nil {
		conversationFailure(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"conversation": conversationDTO(row), "messages": storedMessages(msgs)})
}

// RenameConversation serves PUT /assistant/conversations/:id.
func (r *AssistantRouter) RenameConversation(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req struct {
		Title string `json:"title"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := r.Service.RenameConversation(withOwner(c), id, req.Title); err != nil {
		conversationFailure(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

// DeleteConversation serves DELETE /assistant/conversations/:id.
func (r *AssistantRouter) DeleteConversation(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	n, err := r.Service.DeleteConversations(withOwner(c), []uint{id})
	if err != nil {
		conversationFailure(c, err)
		return
	}
	if n == 0 {
		query.ErrorResponse(c, http.StatusNotFound, assistant.ErrNoConversation)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

// BatchDeleteConversations serves POST /assistant/conversations/batch-delete.
func (r *AssistantRouter) BatchDeleteConversations(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if len(req.IDs) > 200 {
		query.BadRequest(c, errors.New("at most 200 conversations at a time"))
		return
	}
	n, err := r.Service.DeleteConversations(withOwner(c), req.IDs)
	if err != nil {
		conversationFailure(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"deleted": n})
}
