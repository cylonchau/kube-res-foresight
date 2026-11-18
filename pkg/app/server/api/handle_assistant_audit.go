package api

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/gin-gonic/gin"
)

// AssistantAuditToolDTO is a tool a question read.
type AssistantAuditToolDTO struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
}

// AssistantAuditDTO is an audit record of a question to the assistant as the page lists it. It has no text of the question, the answer or a
// tool result: the platform keeps none in the audit.
type AssistantAuditDTO struct {
	ID               uint                    `json:"id"`
	CreatedAt        time.Time               `json:"created_at"`
	UserID           uint                    `json:"user_id"`
	UserName         string                  `json:"user_name"`
	Source           string                  `json:"source"`
	ConversationID   uint                    `json:"conversation_id"`
	Page             string                  `json:"page"`
	ConnectionID     uint                    `json:"connection_id"`
	Model            string                  `json:"model"`
	ToolMode         string                  `json:"tool_mode"`
	Status           string                  `json:"status"`
	Error            string                  `json:"error"`
	DurationMS       int64                   `json:"duration_ms"`
	LLMCalls         int                     `json:"llm_calls"`
	PromptTokens     int                     `json:"prompt_tokens"`
	CompletionTokens int                     `json:"completion_tokens"`
	Tools            []AssistantAuditToolDTO `json:"tools"`
	Refused          []string                `json:"refused"`
}

func auditDTO(a *model.AssistantAudit) AssistantAuditDTO {
	out := AssistantAuditDTO{ID: a.ID, CreatedAt: a.CreatedAt, UserID: a.UserID, UserName: a.UserName, Source: a.Source, ConversationID: a.ConversationID,
		Page: a.Page, ConnectionID: a.ConnectionID, Model: a.Model, ToolMode: a.ToolMode, Status: a.Status, Error: a.Error, DurationMS: a.DurationMS,
		LLMCalls: a.LLMCalls, PromptTokens: a.PromptTokens, CompletionTokens: a.CompletionTokens, Tools: []AssistantAuditToolDTO{}, Refused: []string{}}
	if a.Tools != "" {
		_ = json.Unmarshal([]byte(a.Tools), &out.Tools)
	}
	if a.Refused != "" {
		_ = json.Unmarshal([]byte(a.Refused), &out.Refused)
	}
	return out
}

// ListAudits serves GET /assistant/audit?keyword=&status=&source=&refused=&from=&to=&page=&page_size=: the audit records of the assistant, newest
// first. It is for the administrators who hold the menu AI audit; it carries who asked and what the assistant did, never what was said.
func (r *AssistantRouter) ListAudits(c *gin.Context) {
	if r.Service.Audits == nil {
		query.InternalError(c, errors.New("the audit is not set up"))
		return
	}
	from, to := timeQuery(c, "from"), timeQuery(c, "to")
	filter := rdb.AuditFilter{Keyword: c.Query("keyword"), Status: c.Query("status"), Source: c.Query("source"), Refused: c.Query("refused") == "true", From: from, To: to}
	if filter.Status != "" && filter.Status != "success" && filter.Status != "failed" {
		query.BadRequest(c, errors.New("status is success or failed"))
		return
	}
	page, size := pageParams(c)
	list, total, err := r.Service.Audits.List(c.Request.Context(), filter, page, size)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	out := make([]AssistantAuditDTO, 0, len(list))
	for _, row := range list {
		out = append(out, auditDTO(row))
	}
	query.SuccessResponse(c, nil, gin.H{"list": out, "total": total, "page": page, "pageSize": size})
}
