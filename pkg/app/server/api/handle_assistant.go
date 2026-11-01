package api

import (
	"context"
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
	"github.com/cylonchau/kube-resource-fcst/pkg/job/tasks/aitask"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// AssistantRouter serves the AI assistant: whether it is on, one question and its answer, the skills, the export of the service state, and
// the language models and the skills an administrator manages. The conversation is kept by the page: the platform stores none of it.
type AssistantRouter struct {
	Service *assistant.Service
	// Queue asks the task center to run an AI scheduled task now; nil in a process that has no job table to write.
	Queue *aitask.Queue
}

// withOwner puts the signed-in user in the context of a question: with one, the assistant may make and manage that user's scheduled tasks.
func withOwner(c *gin.Context) context.Context {
	ctx := c.Request.Context()
	if owner, ok := ownerOfRequest(c); ok {
		ctx = assistant.WithOwner(ctx, owner)
	}
	return ctx
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
// cannot, and the skills that are on. It never carries a key.
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
	reply, err := r.Service.Chat(withOwner(c), req.Messages, assistantLang(req.Lang))
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
	reply, err := r.Service.RunSkill(withOwner(c), strings.TrimPrefix(strings.TrimSpace(req.Name), "/"), req.Args, assistantLang(req.Lang))
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

// ---- the language models of the assistant (LLM management)

// AssistantConnectionReq is what the form of a language model sends. The key is sent only when it is set or changed: empty, or the mask,
// keeps the stored one; so does a header value that is the mask.
type AssistantConnectionReq struct {
	ID             uint               `json:"id"`
	Name           string             `json:"name"`
	Description    string             `json:"description"`
	Provider       string             `json:"provider"`
	Endpoint       string             `json:"endpoint"`
	Model          string             `json:"model"`
	APIKey         string             `json:"api_key"`
	Enabled        bool               `json:"enabled"`
	IsDefault      bool               `json:"is_default"`
	TimeoutSeconds int                `json:"timeout_seconds"`
	SkipTLSVerify  bool               `json:"skip_tls_verify"`
	ProxyURL       string             `json:"proxy_url"`
	Headers        []assistant.Header `json:"headers"`
	CustomParams   string             `json:"custom_params"`
	Temperature    *float64           `json:"temperature"`
	MaxTokens      int                `json:"max_tokens"`
	ContextLength  int                `json:"context_length"`
	MaxToolRounds  int                `json:"max_tool_rounds"`
}

func (q AssistantConnectionReq) input() assistant.ConnectionInput {
	return assistant.ConnectionInput{Name: q.Name, Description: q.Description, Provider: q.Provider, Endpoint: q.Endpoint, Model: q.Model, APIKey: q.APIKey,
		Enabled: q.Enabled, IsDefault: q.IsDefault, TimeoutSecond: q.TimeoutSeconds, SkipTLSVerify: q.SkipTLSVerify, ProxyURL: q.ProxyURL, Headers: q.Headers,
		CustomParams: q.CustomParams, Temperature: q.Temperature, MaxTokens: q.MaxTokens, ContextLength: q.ContextLength, MaxToolRounds: q.MaxToolRounds}
}

// AssistantConnectionDTO is a language model as the page sees it: the key is never returned, only whether there is one, and the values of
// the headers are the mask.
type AssistantConnectionDTO struct {
	ID             uint               `json:"id"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
	Name           string             `json:"name"`
	Description    string             `json:"description"`
	Provider       string             `json:"provider"`
	Endpoint       string             `json:"endpoint"`
	Model          string             `json:"model"`
	Enabled        bool               `json:"enabled"`
	IsDefault      bool               `json:"is_default"`
	HasAPIKey      bool               `json:"has_api_key"`
	TimeoutSeconds int                `json:"timeout_seconds"`
	SkipTLSVerify  bool               `json:"skip_tls_verify"`
	ProxyURL       string             `json:"proxy_url"`
	Headers        []assistant.Header `json:"headers"`
	CustomParams   string             `json:"custom_params"`
	Temperature    *float64           `json:"temperature"`
	MaxTokens      int                `json:"max_tokens"`
	ContextLength  int                `json:"context_length"`
	MaxToolRounds  int                `json:"max_tool_rounds"`
}

func connectionDTO(row *model.AssistantConnection) AssistantConnectionDTO {
	return AssistantConnectionDTO{ID: row.ID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, Name: row.Name, Description: row.Description, Provider: row.Provider,
		Endpoint: row.Endpoint, Model: row.Model, Enabled: row.Enabled, IsDefault: row.IsDefault, HasAPIKey: row.APIKey != "", TimeoutSeconds: row.TimeoutSeconds,
		SkipTLSVerify: row.SkipTLSVerify, ProxyURL: row.ProxyURL, Headers: assistant.StoredHeaderNames(row), CustomParams: row.CustomParams,
		Temperature: row.Temperature, MaxTokens: row.MaxTokens, ContextLength: row.ContextLength, MaxToolRounds: row.MaxToolRounds}
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

// ConnectionOptions serves GET /assistant/connection-options: the enabled language models, for the selector of a skill.
func (r *AssistantRouter) ConnectionOptions(c *gin.Context) {
	list, err := r.Service.Connections.Options(c.Request.Context())
	if err != nil {
		query.InternalError(c, err)
		return
	}
	type option struct {
		ID        uint   `json:"id"`
		Name      string `json:"name"`
		Model     string `json:"model"`
		IsDefault bool   `json:"is_default"`
	}
	out := make([]option, 0, len(list))
	for _, row := range list {
		out = append(out, option{ID: row.ID, Name: row.Name, Model: row.Model, IsDefault: row.IsDefault})
	}
	query.SuccessResponse(c, nil, out)
}

func (r *AssistantRouter) saveConnection(c *gin.Context, row *model.AssistantConnection, req AssistantConnectionReq) {
	if err := assistant.Apply(row, req.input()); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := r.Service.Connections.Save(c.Request.Context(), row); err != nil {
		if errors.Is(err, rdb.ErrNameTaken) {
			query.BadRequest(c, errors.New("a language model with this name already exists"))
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
// question and says whether it answered. The key and the header values of a stored connection are used where the form leaves them out.
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
			row.APIKey, row.Headers = stored.APIKey, stored.Headers
		}
	}
	if err := assistant.Apply(row, req.input()); err != nil {
		query.BadRequest(c, err)
		return
	}
	query.SuccessResponse(c, nil, r.Service.Probe(c.Request.Context(), assistant.FromRow(row)))
}

// ---- the skills of the assistant (Skill management)

// AssistantSkillFormReq is what the form of a skill sends. A built-in skill takes only enabled and llm_id.
type AssistantSkillFormReq struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	ArgMode     string `json:"arg_mode"`
	ArgHint     string `json:"arg_hint"`
	LLMID       uint   `json:"llm_id"`
	Enabled     bool   `json:"enabled"`
}

// ListSkills serves GET /assistant/skills?keyword=&enabled=&kind=builtin|custom&page=&page_size=.
func (r *AssistantRouter) ListSkills(c *gin.Context) {
	ctx := c.Request.Context()
	if err := r.Service.EnsureBuiltinSkills(ctx); err != nil {
		query.InternalError(c, err)
		return
	}
	all, err := r.Service.Skills(ctx)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	kw := strings.ToLower(strings.TrimSpace(c.Query("keyword")))
	enabled := boolQuery(c, "enabled")
	kind := c.Query("kind")
	var rows []assistant.SkillView
	for _, v := range all {
		switch {
		case enabled != nil && v.Enabled != *enabled:
		case kind == "builtin" && !v.Builtin, kind == "custom" && v.Builtin:
		case kw != "" && !strings.Contains(strings.ToLower(v.Name+" "+v.Title+" "+v.Description), kw):
		default:
			rows = append(rows, v)
		}
	}
	page, size := pageParams(c)
	total := len(rows)
	from, to := (page-1)*size, page*size
	if from > total {
		from = total
	}
	if to > total {
		to = total
	}
	out := rows[from:to]
	if out == nil {
		out = []assistant.SkillView{}
	}
	query.SuccessResponse(c, nil, gin.H{"list": out, "total": total, "page": page, "pageSize": size})
}

// checkSkillModel refuses a skill that names a language model that does not exist.
func (r *AssistantRouter) checkSkillModel(c *gin.Context, id uint) bool {
	if id == 0 {
		return true
	}
	if _, err := r.Service.Connections.Get(c.Request.Context(), id); err != nil {
		query.BadRequest(c, errors.New("the language model chosen for the skill does not exist"))
		return false
	}
	return true
}

func (r *AssistantRouter) saveSkill(c *gin.Context, row *model.AssistantSkill) {
	if err := r.Service.SkillStore.Save(c.Request.Context(), row); err != nil {
		if errors.Is(err, rdb.ErrNameTaken) {
			query.BadRequest(c, errors.New("a skill with this command already exists"))
			return
		}
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, row)
}

// CreateSkill serves POST /assistant/skills: a custom skill.
func (r *AssistantRouter) CreateSkill(c *gin.Context) {
	var req AssistantSkillFormReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	row := &model.AssistantSkill{}
	if err := assistant.ApplySkill(row, assistant.SkillInput{Name: req.Name, Title: req.Title, Description: req.Description, Prompt: req.Prompt,
		ArgMode: req.ArgMode, ArgHint: req.ArgHint, LLMID: req.LLMID, Enabled: req.Enabled}); err != nil {
		query.BadRequest(c, err)
		return
	}
	if r.checkSkillModel(c, row.LLMID) {
		r.saveSkill(c, row)
	}
}

// UpdateSkill serves PUT /assistant/skills/:id. A built-in skill takes only whether it is on and which model it asks.
func (r *AssistantRouter) UpdateSkill(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req AssistantSkillFormReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	row, err := r.Service.SkillStore.Get(c.Request.Context(), id)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	if row.Builtin {
		row.Enabled, row.LLMID = req.Enabled, req.LLMID
	} else if err := assistant.ApplySkill(row, assistant.SkillInput{Name: req.Name, Title: req.Title, Description: req.Description, Prompt: req.Prompt,
		ArgMode: req.ArgMode, ArgHint: req.ArgHint, LLMID: req.LLMID, Enabled: req.Enabled}); err != nil {
		query.BadRequest(c, err)
		return
	}
	if r.checkSkillModel(c, row.LLMID) {
		r.saveSkill(c, row)
	}
}

// DeleteSkill serves DELETE /assistant/skills/:id (a custom skill only).
func (r *AssistantRouter) DeleteSkill(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	row, err := r.Service.SkillStore.Get(c.Request.Context(), id)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	if row.Builtin {
		query.BadRequest(c, errors.New("a built-in skill cannot be deleted: switch it off instead"))
		return
	}
	if err := r.Service.SkillStore.Delete(c.Request.Context(), []uint{id}); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

// BatchDeleteSkills serves POST /assistant/skills/batch-delete; the built-in skills among the ids are left alone.
func (r *AssistantRouter) BatchDeleteSkills(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := r.Service.SkillStore.Delete(c.Request.Context(), req.IDs); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}
