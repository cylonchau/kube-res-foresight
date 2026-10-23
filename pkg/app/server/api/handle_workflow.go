package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/app/query"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/logger"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/workflow"
	"github.com/gin-gonic/gin"
	"strconv"
	"strings"
)

type WorkflowRouter struct {
	CommandDAO  *rdb.BotCommandDAO
	WorkflowDAO *rdb.WorkflowDefDAO
	InstanceDAO *rdb.WorkflowInstanceDAO
	Engine      *workflow.Engine
	UserDAO     *rdb.UserDAO
	// BotDAO (optional) validates the bot a command is bound to.
	BotDAO *rdb.TelegramBotDAO
}

// RegisterRoutes mounts every admin endpoint this router owns onto rg, which
// must already be scoped at ".../telegram" (see pkg/app/router/router.go,
// where rg is telegramProtectedGroup) — every route here lives under its
// "/admin" subpath.
func (r *WorkflowRouter) RegisterRoutes(rg *gin.RouterGroup) {
	tg := rg.Group("/admin")
	{
		tg.GET("/commands", r.AdminListBotCommands)
		tg.POST("/commands", r.AdminCreateBotCommand)
		tg.POST("/commands/batch-delete", r.AdminBatchDeleteBotCommands)
		tg.PUT("/commands/:id", r.AdminUpdateBotCommand)
		tg.DELETE("/commands/:id", r.AdminDeleteBotCommand)
		tg.GET("/workflow-handlers", r.AdminListRegisteredHandlers)
		tg.GET("/workflow-option-sources/:source", r.AdminListOptionSourceValues)

		tg.GET("/workflows", r.AdminListWorkflowDefs)
		tg.GET("/workflows/:key", r.AdminGetWorkflowDef)
		tg.POST("/workflows", r.AdminCreateWorkflowDef)
		tg.POST("/workflows/batch-delete", r.AdminBatchDeleteWorkflowDefs)
		tg.PUT("/workflows/:key", r.AdminUpdateWorkflowDef)
		tg.DELETE("/workflows/:key", r.AdminDeleteWorkflowDef)

		tg.GET("/workflow-instances", r.AdminListWorkflowInstances)
		tg.POST("/workflow-instances/batch-delete", r.AdminBatchDeleteWorkflowInstances)
		tg.GET("/workflow-instances/:id", r.AdminGetWorkflowInstance)
		tg.POST("/workflow-instances/:id/action", r.AdminExecuteWorkflowAction)
		tg.POST("/workflow-instances/:id/transition", r.AdminDispatchWorkflowTransition)
		tg.DELETE("/workflow-instances/:id", r.AdminDeleteWorkflowInstance)
	}
}

// Bot Commands API

func (r *WorkflowRouter) AdminListBotCommands(c *gin.Context) {
	if !hasAnyRole(c, "ADMIN") {
		query.Forbidden(c, query.ErrNoPermission)
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	keyword := c.Query("keyword")
	// bot_id narrows the list to one bot's commands (0 = the ones shared by every bot).
	var botID *int64
	if raw := c.Query("bot_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id < 0 {
			query.BadRequest(c, errors.New("invalid bot_id"))
			return
		}
		botID = &id
	}

	list, total, err := r.CommandDAO.ListFiltered(c.Request.Context(), page, pageSize, keyword, botID)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{
		"list":      list,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

type BotCommandReq struct {
	// BotID binds the command to one Telegram bot; 0 applies to every bot.
	BotID             int64  `json:"bot_id"`
	Command           string `json:"command" binding:"required"`
	Name              string `json:"name" binding:"required"`
	Pattern           string `json:"pattern"`
	Description       string `json:"description"`
	RequiredRolesJSON string `json:"required_roles_json"`
	// ActionType: "workflow" (default, blank also means workflow) | "reply"
	// | "webhook" | "handler" | "builtin" — see model.BotCommandDef's doc comment.
	ActionType     string `json:"action_type"`
	WorkflowKey    string `json:"workflow_key"`
	ParamsJSON     string `json:"params_json"`
	RequireBinding bool   `json:"require_binding"`
	// RequiredParamsJSON is a JSON object of {var_key: label} pairs
	// MatchCommand's extracted vars must contain before ExecuteCommand runs
	// — see model.BotCommandDef.RequiredParamsJSON.
	RequiredParamsJSON string `json:"required_params_json"`
	// RelatedTemplateKeysJSON is a JSON array of NotifyTemplate keys the
	// admin has associated with this command — see
	// model.BotCommandDef.RelatedTemplateKeysJSON.
	RelatedTemplateKeysJSON string `json:"related_template_keys_json"`
	Enabled                 bool   `json:"enabled"`
}

// validateBotCommandReq enforces the field req.action_type actually needs,
// since only "workflow" can rely on a NOT NULL/binding:"required"-style
// column-level check now that WorkflowKey is optional. For "handler" it
// also checks the referenced handler_name is a real, registered
// CommandHandler — rejecting the save here (with a clear reason) is the
// whole point: it replaces "configure a command that silently does nothing
// because the handler name was never registered" with an immediate error.
func validateBotCommandReq(ctx context.Context, engine *workflow.Engine, workflowDAO *rdb.WorkflowDefDAO, req *BotCommandReq) error {
	actionType := req.ActionType
	if actionType == "" {
		actionType = workflow.CommandActionWorkflow
	}
	var params map[string]string
	_ = json.Unmarshal([]byte(req.ParamsJSON), &params)

	switch actionType {
	case workflow.CommandActionWorkflow:
		if req.WorkflowKey == "" {
			return errors.New("workflow_key is required when action_type is \"workflow\"")
		}
		// A key that names no pipeline is the same class of mistake as a
		// handler name that was never registered: the console shows the
		// command as wired up, and it fails only when someone finally sends
		// it. The seeded /start and /report commands sat like this for a
		// while, pointing at "bot_start_flow"/"bot_report_flow", pipelines
		// that never existed.
		if workflowDAO != nil {
			if def, err := workflowDAO.GetByKey(ctx, req.WorkflowKey); err != nil || def == nil {
				return fmt.Errorf("no workflow pipeline with key %q — create it first, or use action_type %q for a command the chat driver serves itself",
					req.WorkflowKey, workflow.CommandActionBuiltin)
			}
		}
	case workflow.CommandActionBuiltin:
		// Served by the chat driver before the engine sees it, so there is
		// nothing here to point at and nothing to validate.
	case workflow.CommandActionReply:
		if params["template_key"] == "" {
			return errors.New("params_json.template_key is required when action_type is \"reply\"")
		}
	case workflow.CommandActionWebhook:
		if params["url"] == "" && params["webhook_url"] == "" {
			return errors.New("params_json.url is required when action_type is \"webhook\"")
		}
	case workflow.CommandActionHandler:
		name := params["handler_name"]
		if name == "" {
			return errors.New("params_json.handler_name is required when action_type is \"handler\"")
		}
		if engine != nil {
			registered := false
			for _, n := range engine.RegisteredCommandHandlerNames() {
				if n == name {
					registered = true
					break
				}
			}
			if !registered {
				return fmt.Errorf("no registered command handler named %q — see GET .../admin/workflow-handlers for the real list", name)
			}
		}
	default:
		return fmt.Errorf("unknown action_type %q (expected workflow|builtin|reply|webhook|handler)", actionType)
	}

	if req.RequiredParamsJSON != "" {
		var required map[string]string
		if err := json.Unmarshal([]byte(req.RequiredParamsJSON), &required); err != nil {
			return fmt.Errorf("required_params_json must be a JSON object of {var_key: label}: %w", err)
		}
	}
	if req.RelatedTemplateKeysJSON != "" {
		var keys []string
		if err := json.Unmarshal([]byte(req.RelatedTemplateKeysJSON), &keys); err != nil {
			return fmt.Errorf("related_template_keys_json must be a JSON array of NotifyTemplate keys: %w", err)
		}
	}
	return nil
}

// botExists rejects a command bound to a bot that does not exist. A zero id
// (every bot) and a router without a bot store always pass.
func (r *WorkflowRouter) botExists(c *gin.Context, botID int64) bool {
	if botID == 0 || r.BotDAO == nil {
		return true
	}
	if _, err := r.BotDAO.Get(c.Request.Context(), botID); err != nil {
		query.BadRequest(c, errors.New("the bot does not exist"))
		return false
	}
	return true
}

func (r *WorkflowRouter) AdminCreateBotCommand(c *gin.Context) {
	if !hasAnyRole(c, "ADMIN") {
		query.Forbidden(c, query.ErrNoPermission)
		return
	}
	var req BotCommandReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := validateBotCommandReq(c.Request.Context(), r.Engine, r.WorkflowDAO, &req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if !r.botExists(c, req.BotID) {
		return
	}
	item := &model.BotCommandDef{
		BotID:                   req.BotID,
		Command:                 req.Command,
		Name:                    req.Name,
		Pattern:                 req.Pattern,
		Description:             req.Description,
		RequiredRolesJSON:       req.RequiredRolesJSON,
		ActionType:              req.ActionType,
		WorkflowKey:             req.WorkflowKey,
		ParamsJSON:              req.ParamsJSON,
		RequireBinding:          req.RequireBinding,
		RequiredParamsJSON:      req.RequiredParamsJSON,
		RelatedTemplateKeysJSON: req.RelatedTemplateKeysJSON,
		Enabled:                 req.Enabled,
	}
	if err := r.CommandDAO.Create(c.Request.Context(), item); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, item)
}

func (r *WorkflowRouter) AdminUpdateBotCommand(c *gin.Context) {
	if !hasAnyRole(c, "ADMIN") {
		query.Forbidden(c, query.ErrNoPermission)
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		query.BadRequest(c, errors.New("invalid command id"))
		return
	}
	var req BotCommandReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := validateBotCommandReq(c.Request.Context(), r.Engine, r.WorkflowDAO, &req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if !r.botExists(c, req.BotID) {
		return
	}
	updates := map[string]interface{}{
		"bot_id":                     req.BotID,
		"command":                    req.Command,
		"name":                       req.Name,
		"pattern":                    req.Pattern,
		"description":                req.Description,
		"required_roles_json":        req.RequiredRolesJSON,
		"action_type":                req.ActionType,
		"workflow_key":               req.WorkflowKey,
		"params_json":                req.ParamsJSON,
		"require_binding":            req.RequireBinding,
		"required_params_json":       req.RequiredParamsJSON,
		"related_template_keys_json": req.RelatedTemplateKeysJSON,
		"enabled":                    req.Enabled,
	}
	if err := r.CommandDAO.Update(c.Request.Context(), id, updates); err != nil {
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"id": id, "updated": true})
}

// AdminListRegisteredHandlers exposes the real catalog of backend-registered
// handlers — output-action types (workflow.Engine.RegisterOutputActionHandler)
// and command handlers (RegisterCommandHandler) — so the admin console can
// offer a real picker instead of the admin having to guess or read Go
// source, and so validateBotCommandReq's "handler_name must be registered"
// check has something visible to point at.
// @Summary List registered workflow/command handlers (admin)
// @Tags Telegram
// @Produce json
// @Success 200 {object} query.Response "Success"
// @Router /api/v1/telegram/admin/workflow-handlers [get]
func (r *WorkflowRouter) AdminListRegisteredHandlers(c *gin.Context) {
	if !hasAnyRole(c, "ADMIN") {
		query.Forbidden(c, query.ErrNoPermission)
		return
	}
	if r.Engine == nil {
		query.SuccessResponse(c, nil, gin.H{"output_actions": []string{}, "commands": []string{}})
		return
	}
	query.SuccessResponse(c, nil, gin.H{
		"output_actions": r.Engine.RegisteredOutputActionTypes(),
		"commands":       r.Engine.RegisteredCommandHandlerNames(),
		"option_sources": r.Engine.RegisteredOptionSourceNames(),
	})
}

// AdminListOptionSourceValues resolves one registered option source (see
// workflow.Engine.RegisterOptionSource) to its current choices, so a
// pipeline's manual_field declared with `options_source` renders a real
// dropdown of live data registered by the owning module —
// instead of asking the operator to type a raw database ID.
// @Summary Resolve a workflow select field's option source (admin)
// @Tags Telegram
// @Produce json
// @Param source path string true "Registered option source name"
// @Success 200 {object} query.Response "Success"
// @Router /api/v1/telegram/admin/workflow-option-sources/{source} [get]
func (r *WorkflowRouter) AdminListOptionSourceValues(c *gin.Context) {
	if !hasAnyRole(c, "ADMIN", "APPROVER", "OPS") {
		query.Forbidden(c, query.ErrNoPermission)
		return
	}
	if r.Engine == nil {
		query.SuccessResponse(c, nil, gin.H{"options": []workflow.SelectOption{}})
		return
	}
	source := strings.TrimSpace(c.Param("source"))
	options, err := r.Engine.ResolveOptionSource(c.Request.Context(), source)
	if err != nil {
		query.BadRequest(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"options": options})
}

func (r *WorkflowRouter) AdminDeleteBotCommand(c *gin.Context) {
	if !hasAnyRole(c, "ADMIN") {
		query.Forbidden(c, query.ErrNoPermission)
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		query.BadRequest(c, errors.New("invalid command id"))
		return
	}
	if err := r.CommandDAO.Delete(c.Request.Context(), id); err != nil {
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

type BotCommandBatchDeleteReq struct {
	IDs []int64 `json:"ids" binding:"required"`
}

func (r *WorkflowRouter) AdminBatchDeleteBotCommands(c *gin.Context) {
	if !hasAnyRole(c, "ADMIN") {
		query.Forbidden(c, query.ErrNoPermission)
		return
	}
	var req BotCommandBatchDeleteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := r.CommandDAO.BatchDelete(c.Request.Context(), req.IDs); err != nil {
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

// Workflow Definitions API

// AdminListWorkflowDefs and AdminGetWorkflowDef are read-only, and are read
// by more than the admin console: a ticket detail view (web or the TMA
// mobile client) needs a pipeline's manual_fields/transitions/guard_role to
// render its action form at all, so APPROVER/OPS — the roles that actually
// act on tickets — need this too, not just ADMIN. Only Create/Update/Delete
// below stay ADMIN-only.
func (r *WorkflowRouter) AdminListWorkflowDefs(c *gin.Context) {
	if !hasAnyRole(c, "ADMIN", "OPS", "APPROVER") {
		query.Forbidden(c, query.ErrNoPermission)
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	keyword := c.Query("keyword")

	list, total, err := r.WorkflowDAO.ListPaginated(c.Request.Context(), page, pageSize, keyword)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{
		"list":      list,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (r *WorkflowRouter) AdminGetWorkflowDef(c *gin.Context) {
	if !hasAnyRole(c, "ADMIN", "OPS", "APPROVER") {
		query.Forbidden(c, query.ErrNoPermission)
		return
	}
	key := c.Param("key")
	item, err := r.WorkflowDAO.GetByKey(c.Request.Context(), key)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, item)
}

// validatePipelineJSON checks that a workflow definition's pipeline_json
// parses against the expected schema, shared by create and update.
func validatePipelineJSON(pipelineJSON string) error {
	var pipeline workflow.Pipeline
	if err := json.Unmarshal([]byte(pipelineJSON), &pipeline); err != nil {
		return errors.New("invalid pipeline JSON schema")
	}
	return nil
}

type WorkflowDefReq struct {
	WorkflowKey string `json:"workflow_key" binding:"required"`
	Name        string `json:"name" binding:"required"`
	// Driver names the chat platform this pipeline's notifications go out
	// through ("telegram", "msteams", "slack", "generic"). It is a real
	// column on WorkflowDef, but was missing from this request struct, so
	// the console's 通信驱动 picker was silently discarded on every save and
	// the column always displayed the default.
	Driver       string `json:"driver"`
	Description  string `json:"description"`
	InitialState string `json:"initial_state" binding:"required"`
	PipelineJSON string `json:"pipeline_json" binding:"required"`
	Enabled      bool   `json:"enabled"`
}

func (r *WorkflowRouter) AdminCreateWorkflowDef(c *gin.Context) {
	if !hasAnyRole(c, "ADMIN") {
		query.Forbidden(c, query.ErrNoPermission)
		return
	}
	var req WorkflowDefReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}

	if err := validatePipelineJSON(req.PipelineJSON); err != nil {
		query.BadRequest(c, err)
		return
	}

	driver := req.Driver
	if driver == "" {
		driver = "telegram"
	}
	item := &model.WorkflowDef{
		WorkflowKey:  req.WorkflowKey,
		Name:         req.Name,
		Driver:       driver,
		Description:  req.Description,
		InitialState: req.InitialState,
		PipelineJSON: req.PipelineJSON,
		Enabled:      req.Enabled,
	}
	if err := r.WorkflowDAO.Create(c.Request.Context(), item); err != nil {
		query.InternalError(c, err)
		return
	}
	if !req.Enabled {
		// WorkflowDef.Enabled is `default:true`, and GORM omits a Go zero
		// value from the INSERT — so a pipeline created as disabled would come
		// back enabled. Write the false explicitly.
		if _, err := r.WorkflowDAO.Update(c.Request.Context(), item.WorkflowKey, map[string]interface{}{"enabled": false}); err != nil {
			query.InternalError(c, err)
			return
		}
		item.Enabled = false
	}
	query.SuccessResponse(c, nil, item)
}

func (r *WorkflowRouter) AdminUpdateWorkflowDef(c *gin.Context) {
	if !hasAnyRole(c, "ADMIN") {
		query.Forbidden(c, query.ErrNoPermission)
		return
	}
	key := c.Param("key")
	var req WorkflowDefReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}

	if err := validatePipelineJSON(req.PipelineJSON); err != nil {
		query.BadRequest(c, err)
		return
	}

	updates := map[string]interface{}{
		"name":          req.Name,
		"description":   req.Description,
		"initial_state": req.InitialState,
		"pipeline_json": req.PipelineJSON,
		"enabled":       req.Enabled,
	}
	if req.Driver != "" {
		updates["driver"] = req.Driver
	}
	// Updates() matching zero rows is not an error to GORM, so a PUT to a
	// key that doesn't exist used to return {"updated": true} and the console
	// showed "更新成功" over a database that had not changed. Report it.
	affected, err := r.WorkflowDAO.Update(c.Request.Context(), key, updates)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	if affected == 0 {
		query.NotFound(c, fmt.Errorf("no workflow pipeline with key %q", key))
		return
	}
	query.SuccessResponse(c, nil, gin.H{"key": key, "updated": true})
}

func (r *WorkflowRouter) AdminDeleteWorkflowDef(c *gin.Context) {
	if !hasAnyRole(c, "ADMIN") {
		query.Forbidden(c, query.ErrNoPermission)
		return
	}
	key := c.Param("key")
	if err := r.WorkflowDAO.Delete(c.Request.Context(), key); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

type WorkflowDefBatchDeleteReq struct {
	Keys []string `json:"keys" binding:"required"`
}

func (r *WorkflowRouter) AdminBatchDeleteWorkflowDefs(c *gin.Context) {
	if !hasAnyRole(c, "ADMIN") {
		query.Forbidden(c, query.ErrNoPermission)
		return
	}
	var req WorkflowDefBatchDeleteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := r.WorkflowDAO.BatchDelete(c.Request.Context(), req.Keys); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

// Workflow Instances API

// AdminListWorkflowInstances and AdminGetWorkflowInstance are read-only and
// open to APPROVER too, not just ADMIN/OPS — an approver otherwise has no
// way to even SEE the ticket they're meant to approve outside of the
// Telegram card, which is exactly what a ticket browser (web or TMA) needs
// to fix. Only AdminDeleteWorkflowInstance below stays ADMIN-only.
func (r *WorkflowRouter) AdminListWorkflowInstances(c *gin.Context) {
	if !hasAnyRole(c, "ADMIN", "OPS", "APPROVER") {
		query.Forbidden(c, query.ErrNoPermission)
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	wfKey := c.Query("workflow_key")
	status := c.Query("status")
	requesterName := c.Query("requester_name")

	list, total, err := r.InstanceDAO.ListPaginated(c.Request.Context(), page, pageSize, wfKey, status, requesterName)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{
		"list":      list,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (r *WorkflowRouter) AdminGetWorkflowInstance(c *gin.Context) {
	if !hasAnyRole(c, "ADMIN", "OPS", "APPROVER") {
		query.Forbidden(c, query.ErrNoPermission)
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		query.BadRequest(c, errors.New("invalid instance id"))
		return
	}
	inst, err := r.InstanceDAO.GetByID(c.Request.Context(), id)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, inst)
}

type WorkflowManualActionReq struct {
	ActionType string            `json:"action_type" binding:"required"`
	Payload    map[string]string `json:"payload"`
}

// AdminExecuteWorkflowAction force-runs a registered OutputActionHandler
// (see workflow.Engine.RegisterOutputActionHandler) directly by type name,
// regardless of the instance's current state or pipeline definition — an
// operator/debug escape hatch, not a way to advance a ticket through its
// pipeline (ExecuteManualAction does not change CurrentState). To submit a
// real transition — including one that carries a required note or
// structured ManualFields — use
// AdminDispatchWorkflowTransition below instead.
func (r *WorkflowRouter) AdminExecuteWorkflowAction(c *gin.Context) {
	if !hasAnyRole(c, "ADMIN", "OPS") {
		query.Forbidden(c, query.ErrNoPermission)
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		query.BadRequest(c, errors.New("invalid instance id"))
		return
	}
	var req WorkflowManualActionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}

	operatorName := "unknown"
	userIdRaw, exists := c.Get("userId")
	resolved := false
	if exists && r.UserDAO != nil {
		if uid, ok := userIdRaw.(uint); ok && uid != 0 {
			if u, err := r.UserDAO.GetByID(c.Request.Context(), uid); err == nil && u != nil {
				operatorName = model.ActorName(u, nil)
				resolved = true
			}
		}
	}
	if !resolved {
		logger.Warn("workflow manual action: could not resolve operator from JWT userId, recording as unknown",
			logger.Int64("instance_id", id))
	}

	if r.Engine != nil {
		inst, err := r.Engine.ExecuteManualAction(c.Request.Context(), id, req.ActionType, operatorName, req.Payload)
		if err != nil {
			query.InternalError(c, err)
			return
		}
		query.SuccessResponse(c, nil, inst)
		return
	}

	query.SuccessResponse(c, nil, "ok")
}

type WorkflowTransitionReq struct {
	Event string            `json:"event" binding:"required"`
	Note  string            `json:"note"`
	Vars  map[string]string `json:"vars"`
}

// AdminDispatchWorkflowTransition submits a real pipeline transition —
// this is the generic, backend-defined execution path for manual workflow actions:
// any ticket type's transition (approve/reject/advance, optionally
// carrying a required note or ManualFields form data) can be fired through
// here without a bespoke endpoint. The pipeline's own guard_role on the
// transition is DispatchEvent's real authorization check; hasAnyRole below
// is only a coarse "must be some kind of operator" gate, same as the other
// workflow-instance admin routes.
// @Summary Submit a workflow transition (admin)
// @Tags Telegram
// @Accept json
// @Produce json
// @Param id path int true "Workflow instance ID"
// @Param request body WorkflowTransitionReq true "Event + optional note/vars"
// @Success 200 {object} query.Response "Success"
// @Router /api/v1/telegram/admin/workflow-instances/{id}/transition [post]
func (r *WorkflowRouter) AdminDispatchWorkflowTransition(c *gin.Context) {
	if !hasAnyRole(c, "ADMIN", "OPS", "APPROVER") {
		query.Forbidden(c, query.ErrNoPermission)
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		query.BadRequest(c, errors.New("invalid instance id"))
		return
	}
	var req WorkflowTransitionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if r.Engine == nil {
		query.InternalError(c, errors.New("workflow engine is not configured"))
		return
	}

	operatorName := "unknown"
	var operatorRoles []string
	if userIdRaw, exists := c.Get("userId"); exists && r.UserDAO != nil {
		if uid, ok := userIdRaw.(uint); ok && uid != 0 {
			if u, err := r.UserDAO.GetByID(c.Request.Context(), uid); err == nil && u != nil {
				operatorName = model.ActorName(u, nil)
				operatorRoles, _ = userAuthorization(u)
			}
		}
	}

	inst, err := r.Engine.DispatchEvent(c.Request.Context(), id, req.Event, operatorName, operatorRoles, 0, req.Note, req.Vars)
	if err != nil {
		// Every failure mode here — invalid event for the current state,
		// unauthorized role, a required note/field missing, a concurrent
		// transition that already won — is "this request can't be applied
		// right now" from the caller's point of view, so it's a 400.
		query.BadRequest(c, err)
		return
	}
	query.SuccessResponse(c, nil, inst)
}

func (r *WorkflowRouter) AdminDeleteWorkflowInstance(c *gin.Context) {
	if !hasAnyRole(c, "ADMIN") {
		query.Forbidden(c, query.ErrNoPermission)
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		query.BadRequest(c, errors.New("invalid instance id"))
		return
	}
	if err := r.InstanceDAO.Delete(c.Request.Context(), id); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

type WorkflowInstanceBatchDeleteReq struct {
	IDs []int64 `json:"ids" binding:"required"`
}

func (r *WorkflowRouter) AdminBatchDeleteWorkflowInstances(c *gin.Context) {
	if !hasAnyRole(c, "ADMIN") {
		query.Forbidden(c, query.ErrNoPermission)
		return
	}
	var req WorkflowInstanceBatchDeleteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	affected, err := r.InstanceDAO.BatchDelete(c.Request.Context(), req.IDs)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"deleted": affected})
}
