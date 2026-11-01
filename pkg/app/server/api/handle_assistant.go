package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/app/query"
	"github.com/cylonchau/kube-resource-fcst/pkg/assistant"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecastsvc"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// AssistantRouter serves the AI assistant: whether it is on, one question and its answer, the skills, the export of the service state, and
// the language model connections it uses. The conversation is kept by the page: the platform stores none of it.
type AssistantRouter struct {
	Service *assistant.Service
}

// AssistantChatReq is the conversation so far; the last message is the question.
type AssistantChatReq struct {
	Messages []assistant.Turn `json:"messages"`
	// Lang is the language of the console, zh or en.
	Lang string `json:"lang"`
}

// AssistantSkillReq runs a skill: its name (without the slash) and what the user typed after it.
type AssistantSkillReq struct {
	Name string `json:"name" binding:"required"`
	Args string `json:"args"`
	Lang string `json:"lang"`
}

func assistantLang(raw string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "en") {
		return "en"
	}
	return "zh"
}

// Status serves GET /assistant/status: whether the assistant is on, whether a language model can be asked and what is missing when it
// cannot, and the skills. It never carries the key.
func (r *AssistantRouter) Status(c *gin.Context) {
	av, err := r.Service.Available(c.Request.Context())
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, av)
}

// reply writes the answer of the assistant, or the failure in its proper status.
func (r *AssistantRouter) reply(c *gin.Context, reply *assistant.Reply, err error) {
	if err == nil {
		query.SuccessResponse(c, nil, reply)
		return
	}
	var notReady assistant.ErrNotReady
	switch {
	case errors.As(err, &notReady):
		query.ErrorResponse(c, http.StatusConflict, err)
	case errors.Is(err, assistant.ErrBusy):
		query.ErrorResponse(c, http.StatusTooManyRequests, err)
	case assistant.IsRequestError(err):
		query.BadRequest(c, err)
	default:
		// the language model could not be reached or refused, or the stored facts could not be read: not a mistake of the caller
		query.ErrorResponse(c, http.StatusBadGateway, err)
	}
}

// Chat serves POST /assistant/chat.
// @Summary Ask the assistant a question about the service
// @Tags Assistant
// @Router /api/v1/assistant/chat [post]
func (r *AssistantRouter) Chat(c *gin.Context) {
	var req AssistantChatReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	reply, err := r.Service.Chat(c.Request.Context(), req.Messages, assistantLang(req.Lang))
	r.reply(c, reply, err)
}

// RunSkill serves POST /assistant/skill: one of the commands of the chat ("/report", "/export", ...).
// @Summary Run a skill of the assistant
// @Tags Assistant
// @Router /api/v1/assistant/skill [post]
func (r *AssistantRouter) RunSkill(c *gin.Context) {
	var req AssistantSkillReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	reply, err := r.Service.RunSkill(c.Request.Context(), strings.TrimPrefix(strings.TrimSpace(req.Name), "/"), req.Args, assistantLang(req.Lang))
	r.reply(c, reply, err)
}

// Export serves GET /assistant/export?format=markdown|json&hours=&lang=: the running state of the service as a file to keep. It is the
// same snapshot the assistant reads, with no secret in it.
// @Summary Export the running state of the service
// @Tags Assistant
// @Router /api/v1/assistant/export [get]
func (r *AssistantRouter) Export(c *gin.Context) {
	hours := 0
	if raw := c.Query("hours"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > forecastsvc.MaxEffectHours {
			query.BadRequest(c, fmt.Errorf("hours must be a whole number between 1 and %d", forecastsvc.MaxEffectHours))
			return
		}
		hours = n
	}
	format := strings.ToLower(c.DefaultQuery("format", "markdown"))
	if format != "markdown" && format != "json" {
		query.BadRequest(c, errors.New("format is markdown or json"))
		return
	}
	report, err := r.Service.Status(c.Request.Context(), hours)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	file := assistant.ExportAttachment(format, report.Hours, "", report.GeneratedAt)
	c.Header("Content-Disposition", `attachment; filename="`+file.Name+`"`)
	c.Header("Cache-Control", "no-store")
	if format == "json" {
		body, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			query.InternalError(c, err)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", body)
		return
	}
	c.Data(http.StatusOK, "text/markdown; charset=utf-8", []byte(report.Markdown(assistantLang(c.Query("lang")))))
}

// ---- the connections of the assistant to a language model

// AssistantConnectionReq is what the form of a connection sends. The key is sent only when it is set or changed: empty, or the mask, keeps
// the stored one.
type AssistantConnectionReq struct {
	ID             uint   `json:"id"`
	Name           string `json:"name"`
	Endpoint       string `json:"endpoint"`
	Model          string `json:"model"`
	APIKey         string `json:"api_key"`
	Enabled        bool   `json:"enabled"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	MaxToolRounds  int    `json:"max_tool_rounds"`
	MaxReplyTokens int    `json:"max_reply_tokens"`
	Remark         string `json:"remark"`
}

func (q AssistantConnectionReq) input() assistant.ConnectionInput {
	return assistant.ConnectionInput{Name: q.Name, Endpoint: q.Endpoint, Model: q.Model, APIKey: q.APIKey, Enabled: q.Enabled,
		TimeoutSeconds: q.TimeoutSeconds, MaxToolRounds: q.MaxToolRounds, MaxReplyTokens: q.MaxReplyTokens, Remark: q.Remark}
}

// AssistantConnectionDTO is a connection as the page sees it: the key is never returned, only whether there is one.
type AssistantConnectionDTO struct {
	ID             uint      `json:"id"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Name           string    `json:"name"`
	Endpoint       string    `json:"endpoint"`
	Model          string    `json:"model"`
	Enabled        bool      `json:"enabled"`
	HasAPIKey      bool      `json:"has_api_key"`
	TimeoutSeconds int       `json:"timeout_seconds"`
	MaxToolRounds  int       `json:"max_tool_rounds"`
	MaxReplyTokens int       `json:"max_reply_tokens"`
	Remark         string    `json:"remark"`
}

func connectionDTO(row *model.AssistantConnection) AssistantConnectionDTO {
	return AssistantConnectionDTO{ID: row.ID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, Name: row.Name, Endpoint: row.Endpoint, Model: row.Model,
		Enabled: row.Enabled, HasAPIKey: row.APIKey != "", TimeoutSeconds: row.TimeoutSeconds, MaxToolRounds: row.MaxToolRounds,
		MaxReplyTokens: row.MaxReplyTokens, Remark: row.Remark}
}

// ListConnections serves GET /assistant/connections?keyword=&enabled=&page=&page_size=.
func (r *AssistantRouter) ListConnections(c *gin.Context) {
	page, size := pageParams(c)
	list, total, err := r.Service.Connections.List(c.Request.Context(), c.Query("keyword"), boolQuery(c, "enabled"), page, size)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	out := make([]AssistantConnectionDTO, 0, len(list))
	for _, row := range list {
		out = append(out, connectionDTO(row))
	}
	query.SuccessResponse(c, nil, gin.H{"list": out, "total": total, "page": page, "pageSize": size})
}

func (r *AssistantRouter) saveConnection(c *gin.Context, row *model.AssistantConnection, req AssistantConnectionReq) {
	if err := assistant.Apply(row, req.input()); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := r.Service.Connections.Save(c.Request.Context(), row); err != nil {
		if errors.Is(err, rdb.ErrNameTaken) {
			query.BadRequest(c, errors.New("a connection with this name already exists"))
			return
		}
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, connectionDTO(row))
}

// CreateConnection serves POST /assistant/connections.
func (r *AssistantRouter) CreateConnection(c *gin.Context) {
	var req AssistantConnectionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	r.saveConnection(c, &model.AssistantConnection{}, req)
}

// UpdateConnection serves PUT /assistant/connections/:id.
func (r *AssistantRouter) UpdateConnection(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req AssistantConnectionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	row, err := r.Service.Connections.Get(c.Request.Context(), id)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	r.saveConnection(c, row, req)
}

// DeleteConnection serves DELETE /assistant/connections/:id.
func (r *AssistantRouter) DeleteConnection(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	if err := r.Service.Connections.Delete(c.Request.Context(), []uint{id}); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

// BatchDeleteConnections serves POST /assistant/connections/batch-delete.
func (r *AssistantRouter) BatchDeleteConnections(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := r.Service.Connections.Delete(c.Request.Context(), req.IDs); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

// TestConnection serves POST /assistant/connections/test: it asks the connection the form holds right now (not the stored one) a small
// question and says whether it answered. The key of a stored connection is used when the form leaves it empty.
func (r *AssistantRouter) TestConnection(c *gin.Context) {
	var req AssistantConnectionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	row := &model.AssistantConnection{}
	if req.ID != 0 {
		stored, err := r.Service.Connections.Get(c.Request.Context(), req.ID)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			query.InternalError(c, err)
			return
		}
		if stored != nil {
			row.APIKey = stored.APIKey
		}
	}
	if err := assistant.Apply(row, req.input()); err != nil {
		query.BadRequest(c, err)
		return
	}
	query.SuccessResponse(c, nil, r.Service.Probe(c.Request.Context(), assistant.FromRow(row)))
}
