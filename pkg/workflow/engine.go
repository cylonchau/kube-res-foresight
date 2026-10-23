package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/logger"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/notify/template"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrNoteRequired is returned by DispatchEvent when the transition being
// fired has RequireNote set but the caller supplied an empty note (e.g. a
// rejection with no reason). pkg/telegram's callback handler catches this
// via CheckTransition (a read-only peek, called before DispatchEvent) to
// switch into a ForceReply prompt instead of showing a generic error.
var ErrNoteRequired = errors.New("workflow: this transition requires a note")

// ErrManualFieldRequired is returned by DispatchEvent when a required
// ManualFields entry on the transition is missing from extraVars.
var ErrManualFieldRequired = errors.New("workflow: required manual field missing")

// BotCommandDef.ActionType values — see model.BotCommandDef's doc comment
// and ExecuteCommand.
const (
	CommandActionWorkflow = "workflow"
	CommandActionReply    = "reply"
	CommandActionWebhook  = "webhook"
	CommandActionHandler  = "handler"
	// CommandActionBuiltin marks a command the chat driver serves itself,
	// before the engine ever sees the message — Telegram's /start (whose
	// "bind_xxx" deep-link payload is a telebot mechanism the engine has no
	// access to) and /report. Their BotCommandDef rows exist so the command
	// still appears in the bot's command menu and stays admin-editable, not
	// because the engine dispatches them.
	//
	// It exists because /start and /report used to be seeded as "workflow"
	// commands pointing at workflow keys ("bot_start_flow",
	// "bot_report_flow") that no WorkflowDef ever defined — the admin
	// console showed them as wired to a pipeline that did not exist, and
	// anything that did route them through the engine would have failed.
	CommandActionBuiltin = "builtin"
)

type SideEffectHandler func(ctx context.Context, inst *model.WorkflowInstance) error

// CommandReply is what a CommandHandler / ExecuteCommand / HandleIncomingText
// return: the text to send back, plus optional buttons attached to that same
// message — the same platform-agnostic Button shape
// ChatDriver.SendMessage already uses for workflow notifications, so a
// handler-type command can attach a button exactly the way a workflow
// notify action does, without knowing anything about Telegram specifically.
type CommandReply struct {
	Text    string
	Buttons []Button
}

// CommandHandler executes a "real code implementation" bot command
// (BotCommandDef.ActionType == "handler"), registered by name via
// RegisterCommandHandler and invoked by ExecuteCommand. vars are
// MatchCommand's extracted variables merged with the command's own
// ParamsJSON (minus "handler_name" itself). requesterUserID is nil when
// the caller isn't bound to a KubeForecast user yet — a handler whose whole job
// is to create that binding (see BotCommandDef.RequireBinding) must expect
// nil here. Returns the CommandReply to send back to the requester (a zero
// value means no reply).
type CommandHandler func(ctx context.Context, vars map[string]string, requesterUserID *uint, requesterTgID int64, requesterName string) (CommandReply, error)

// OutputActionHandler executes one output action. vars is the same mutable
// context-variable map DispatchEvent/ExecuteManualAction is about to persist
// as the instance's ContextDataJSON — a handler that produces a result the
// instance should remember (for example, an external action result) writes it
// into vars, and it lands in the persisted context once the handler returns
// successfully. Returning an error aborts the whole dispatch: the caller
// does not persist any state/context change, so a failed auto action never
// leaves the instance lying about having completed it.
type OutputActionHandler func(ctx context.Context, inst *model.WorkflowInstance, action OutputActionDef, vars map[string]string) error

type Engine struct {
	store           WorkflowStore
	templates       TemplateResolver
	bindings        BindingResolver
	chatDriver      ChatDriver
	pendingReplies  PendingReplyStore
	sideEffects     map[string]SideEffectHandler
	outputHandlers  map[string]OutputActionHandler
	optionSources   map[string]OptionSource
	commandHandlers map[string]CommandHandler
}

// NewEngine constructs the engine. chatDriver may be nil (notify actions
// become a no-op, same nil-safety convention as templates/bindings) —
// callers that don't need outbound chat notifications (e.g. a test that
// only exercises state transitions) don't have to stub one.
func NewEngine(store WorkflowStore, tmpls TemplateResolver, bindings BindingResolver, chatDriver ChatDriver) *Engine {
	e := &Engine{
		store:           store,
		templates:       tmpls,
		bindings:        bindings,
		chatDriver:      chatDriver,
		sideEffects:     make(map[string]SideEffectHandler),
		outputHandlers:  make(map[string]OutputActionHandler),
		optionSources:   make(map[string]OptionSource),
		commandHandlers: make(map[string]CommandHandler),
	}
	e.registerDefaultOutputHandlers()
	return e
}

// SetPendingReplyStore wires the store HandleIncomingText/HandleIncomingAction
// use to track a chat user mid-ForceReply-style prompt. Optional (nil-safe,
// same convention as chatDriver/templates/bindings) — a setter rather than a
// NewEngine parameter so existing call sites (server.go, tests) don't all
// need updating just to opt in.
func (e *Engine) SetPendingReplyStore(s PendingReplyStore) {
	e.pendingReplies = s
}

// SetChatDriver wires (or replaces) the Sender the engine's notify actions
// deliver through. Optional and nil-safe, same convention as
// SetPendingReplyStore — a setter so a chat platform's own package can
// install its driver during its own bootstrap, instead of the process's
// entrypoint having to construct a platform-specific driver just to pass
// it into NewEngine.
func (e *Engine) SetChatDriver(d ChatDriver) {
	e.chatDriver = d
}

func (e *Engine) registerDefaultOutputHandlers() {
	e.RegisterOutputActionHandler("webhook_post", func(ctx context.Context, inst *model.WorkflowInstance, action OutputActionDef, vars map[string]string) error {
		return postWebhook(ctx, action.Params, vars, map[string]interface{}{
			"event":          "workflow_output_action",
			"action_type":    action.Type,
			"action_name":    action.Name,
			"workflow_key":   inst.WorkflowKey,
			"instance_id":    inst.ID,
			"business_type":  inst.BusinessType,
			"business_id":    inst.BusinessID,
			"current_state":  inst.CurrentState,
			"status":         inst.Status,
			"requester_name": inst.RequesterName,
		})
	})
}

// postWebhook POSTs vars (or, if params["body_template"] is set, that
// template rendered against vars) to params["url"]/["webhook_url"], merging
// in defaultPayload's fields when no body_template overrides the whole
// body. Shared by the "webhook_post" OutputActionHandler (workflow output
// actions) and ExecuteCommand's "webhook" command action type — the only
// difference between the two call sites is what they put in defaultPayload.
func postWebhook(ctx context.Context, params map[string]string, vars map[string]string, defaultPayload map[string]interface{}) error {
	targetURL := params["url"]
	if targetURL == "" {
		targetURL = params["webhook_url"]
	}
	if targetURL == "" {
		return errors.New("webhook action requires a 'url' parameter")
	}

	if vars == nil {
		vars = make(map[string]string)
	}

	var bodyBytes []byte
	if customBody, ok := params["body_template"]; ok && customBody != "" {
		_, rendered, _, err := template.Render(template.Source{BodyTemplate: customBody}, vars)
		if err != nil {
			return fmt.Errorf("failed to render webhook body_template: %w", err)
		}
		bodyBytes = []byte(rendered)
	} else {
		payloadObj := defaultPayload
		if payloadObj == nil {
			payloadObj = make(map[string]interface{})
		}
		payloadObj["context_data"] = vars
		payloadObj["timestamp"] = time.Now().Format(time.RFC3339)
		bodyBytes, _ = json.Marshal(payloadObj)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if authHeader, ok := params["auth_header"]; ok && authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	if secret, ok := params["secret"]; ok && secret != "" {
		req.Header.Set("X-KubeForecast-Secret", secret)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		logger.Error("Webhook post failed", logger.String("url", targetURL), logger.Any("err", err))
		return fmt.Errorf("webhook post failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook endpoint returned status %d", resp.StatusCode)
	}
	return nil
}

func (e *Engine) RegisterSideEffect(name string, handler SideEffectHandler) {
	e.sideEffects[name] = handler
}

func (e *Engine) RegisterOutputActionHandler(actionType string, handler OutputActionHandler) {
	e.outputHandlers[actionType] = handler
}

// RegisterCommandHandler registers a "real code implementation" bot command
// handler under name — a BotCommandDef with ActionType "handler" and
// ParamsJSON {"handler_name": name} will invoke it via ExecuteCommand.
func (e *Engine) RegisterCommandHandler(name string, handler CommandHandler) {
	e.commandHandlers[name] = handler
}

// RegisteredOutputActionTypes lists every OutputActionHandler type
// currently registered — used by the admin API to show a real catalog of
// what a pipeline's output_actions/manual_fields can actually reference,
// instead of the admin having to guess or read Go source.
func (e *Engine) RegisteredOutputActionTypes() []string {
	names := make([]string, 0, len(e.outputHandlers))
	for name := range e.outputHandlers {
		names = append(names, name)
	}
	return names
}

// RegisteredCommandHandlerNames lists every CommandHandler name currently
// registered — used by the admin API to populate the "handler_name" picker
// for a BotCommandDef with ActionType "handler", and to validate one
// against a name that's actually wired up before saving.
func (e *Engine) RegisteredCommandHandlerNames() []string {
	names := make([]string, 0, len(e.commandHandlers))
	for name := range e.commandHandlers {
		names = append(names, name)
	}
	return names
}

// MatchCommand finds a configured BotCommandDef matching the user message.
func (e *Engine) MatchCommand(ctx context.Context, text string) (*model.BotCommandDef, map[string]string, error) {
	cmds, err := e.store.ListCommands(ctx)
	if err != nil {
		return nil, nil, err
	}
	trimmed := strings.TrimSpace(text)
	for _, cmd := range cmds {
		if !cmd.Enabled {
			continue
		}
		// Direct slash command prefix match (for example "/run payload").
		if strings.HasPrefix(trimmed, cmd.Command) {
			payload := strings.TrimSpace(strings.TrimPrefix(trimmed, cmd.Command))
			vars := map[string]string{"payload": payload}
			if cmd.Pattern != "" {
				re, err := regexp.Compile(cmd.Pattern)
				if err == nil {
					if match := re.FindStringSubmatch(trimmed); len(match) > 1 {
						setCapturedVars(vars, match[1])
					}
				}
			}
			return &cmd, vars, nil
		}
		// Regex pattern match (e.g. natural language pattern)
		if cmd.Pattern != "" {
			re, err := regexp.Compile(cmd.Pattern)
			if err == nil && re.MatchString(trimmed) {
				vars := map[string]string{}
				if match := re.FindStringSubmatch(trimmed); len(match) > 1 {
					setCapturedVars(vars, match[1])
				}
				return &cmd, vars, nil
			}
		}
	}
	return nil, nil, nil
}

// setCapturedVars exposes the first configured regular-expression capture.
func setCapturedVars(vars map[string]string, raw string) {
	vars["param1"] = strings.TrimSpace(raw)
}

// requiredParamLabels returns the human labels (BotCommandDef.
// RequiredParamsJSON's {var_key: label} values) for every key that's
// missing or blank in vars — checked right after MatchCommand and before
// ExecuteCommand, so a command depending on a captured value fails with one
// clear, actionable reply instead of silently
// starting a workflow instance with that value missing. nil/empty means
// nothing is required or everything required is present.
func requiredParamLabels(cmd *model.BotCommandDef, vars map[string]string) []string {
	if cmd.RequiredParamsJSON == "" {
		return nil
	}
	var required map[string]string
	if err := json.Unmarshal([]byte(cmd.RequiredParamsJSON), &required); err != nil {
		return nil
	}
	missing := make([]string, 0, len(required))
	for key, label := range required {
		if strings.TrimSpace(vars[key]) != "" {
			continue
		}
		if label == "" {
			label = key
		}
		missing = append(missing, label)
	}
	sort.Strings(missing)
	return missing
}

// StartWorkflow creates an instance of workflowKey, sets it to initial_state, and executes entry actions.
func (e *Engine) StartWorkflow(ctx context.Context, workflowKey string, bType string, bID int64, requesterUserID *uint, requesterTgID int64, requesterName string, initialVars map[string]string) (*model.WorkflowInstance, error) {
	def, err := e.store.GetWorkflowDef(ctx, workflowKey)
	if err != nil {
		return nil, fmt.Errorf("workflow %q not found: %w", workflowKey, err)
	}
	if !def.Enabled {
		return nil, fmt.Errorf("workflow %q is disabled", workflowKey)
	}

	var pipeline Pipeline
	if err := json.Unmarshal([]byte(def.PipelineJSON), &pipeline); err != nil {
		return nil, fmt.Errorf("invalid pipeline JSON for %q: %w", workflowKey, err)
	}

	initState := def.InitialState
	if initState == "" {
		initState = pipeline.InitialState
	}

	ctxBytes, _ := json.Marshal(initialVars)
	history := []HistoryEntry{
		{
			FromState: "",
			ToState:   initState,
			Event:     "start",
			Operator:  requesterName,
			Timestamp: time.Now(),
		},
	}
	histBytes, _ := json.Marshal(history)

	inst := &model.WorkflowInstance{
		WorkflowKey:             workflowKey,
		CurrentState:            initState,
		Status:                  "running",
		BusinessType:            bType,
		BusinessID:              bID,
		RequesterUserID:         requesterUserID,
		RequesterTelegramUserID: requesterTgID,
		RequesterName:           requesterName,
		ContextDataJSON:         string(ctxBytes),
		HistoryJSON:             string(histBytes),
	}

	if err := e.store.CreateInstance(ctx, inst); err != nil {
		return nil, err
	}

	// Execute initial state entry actions
	if stateDef, ok := pipeline.States[initState]; ok {
		e.executeActions(ctx, inst, stateDef.EntryActions, &pipeline)
	}

	return inst, nil
}

// ExecuteCommand runs whatever a matched BotCommandDef actually declares —
// starting a workflow, rendering and returning a reply, POSTing a webhook,
// or invoking a registered CommandHandler — so callers (pkg/telegram's
// handleText) dispatch through one entrypoint without knowing which. vars
// is MatchCommand's extracted variables, plus whatever sender-identity keys
// the caller chose to add (e.g. "_sender_username") for handler-type
// commands that need more than a display name. Returns the CommandReply to
// send back to the requester; a zero value means nothing to send (a
// "workflow" command's notifications already went out via its pipeline's
// own entry_actions/ChatDriver).
func (e *Engine) ExecuteCommand(ctx context.Context, cmd *model.BotCommandDef, vars map[string]string, requesterUserID *uint, requesterTgID int64, requesterName string) (CommandReply, error) {
	var params map[string]string
	_ = json.Unmarshal([]byte(cmd.ParamsJSON), &params)

	switch cmd.ActionType {
	case "", CommandActionWorkflow:
		// bType (BusinessType) is the command's own display name, not
		// cmd.WorkflowKey again — the instance already carries WorkflowKey
		// verbatim, so echoing it a second time as "business type" only
		// produced a redundant duplicate column in the admin console (see
		// web/src/views/workflow/instances/index.vue). "业务类型" is meant
		// to say WHAT this ticket is business-wise (e.g. "域名采购申请"),
		// which for a chat-triggered instance is exactly the command's Name.
		_, err := e.StartWorkflow(ctx, cmd.WorkflowKey, cmd.Name, 0, requesterUserID, requesterTgID, requesterName, vars)
		return CommandReply{}, err

	case CommandActionReply:
		templateKey := params["template_key"]
		if templateKey == "" {
			return CommandReply{}, errors.New("reply command requires params_json.template_key")
		}
		if e.templates == nil {
			return CommandReply{}, errors.New("no template resolver configured")
		}
		tmpl, err := e.templates.GetByKey(ctx, templateKey)
		if err != nil || tmpl == nil || !tmpl.Enabled {
			return CommandReply{}, fmt.Errorf("template %q not found or disabled", templateKey)
		}
		mergedVars := mergeVars(vars, map[string]string{"requester": requesterName})
		_, body, buttons, err := template.Render(template.Source{TitleTemplate: tmpl.TitleTemplate, BodyTemplate: tmpl.BodyTemplate, ButtonsJSON: tmpl.ButtonsJSON}, mergedVars)
		return CommandReply{Text: body, Buttons: ButtonsFromTemplate(buttons)}, err

	case CommandActionWebhook:
		mergedVars := mergeVars(vars, map[string]string{"requester": requesterName, "command": cmd.Command})
		err := postWebhook(ctx, params, mergedVars, map[string]interface{}{
			"event":                      "bot_command",
			"command":                    cmd.Command,
			"requester_name":             requesterName,
			"requester_telegram_user_id": requesterTgID,
		})
		if err != nil {
			return CommandReply{}, err
		}
		// Rendered through the same engine as every other action type
		// (supports {{.var}} substitution and {{if}}/{{end}} conditionals),
		// not sent as a raw string — success_message used to bypass
		// template.Render entirely, the one action type whose reply text
		// couldn't use template syntax like everywhere else in this file.
		_, successBody, _, err := template.Render(template.Source{BodyTemplate: params["success_message"]}, mergedVars)
		if err != nil {
			return CommandReply{}, err
		}
		return CommandReply{Text: successBody}, nil

	case CommandActionBuiltin:
		// The driver already handled it (or should have). Reaching here means
		// a driver that doesn't implement this command let it through — say
		// nothing rather than emit an error the user can't act on.
		return CommandReply{}, nil

	case CommandActionHandler:
		handlerName := params["handler_name"]
		handler, ok := e.commandHandlers[handlerName]
		if !ok {
			return CommandReply{}, fmt.Errorf("no command handler registered for %q", handlerName)
		}
		mergedVars := mergeVars(vars, params)
		delete(mergedVars, "handler_name")
		return handler(ctx, mergedVars, requesterUserID, requesterTgID, requesterName)

	default:
		return CommandReply{}, fmt.Errorf("unknown command action_type %q", cmd.ActionType)
	}
}

// mergeVars returns a new map containing base's entries overlaid with
// overrides — neither input is mutated.
func mergeVars(base, overrides map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(overrides))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range overrides {
		merged[k] = v
	}
	return merged
}

// resolveTransition loads instanceID's instance and workflow definition,
// finds the transition event fires from the instance's current state, and
// checks operatorRoles against its guard_role. Shared by DispatchEvent
// (which then executes it) and CheckTransition (a read-only peek).
func (e *Engine) resolveTransition(ctx context.Context, instanceID int64, event string, operatorRoles []string) (*model.WorkflowInstance, *Pipeline, *TransitionDef, error) {
	inst, err := e.store.GetInstance(ctx, instanceID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("instance %d not found: %w", instanceID, err)
	}

	def, err := e.store.GetWorkflowDef(ctx, inst.WorkflowKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("workflow %q not found: %w", inst.WorkflowKey, err)
	}

	var pipeline Pipeline
	if err := json.Unmarshal([]byte(def.PipelineJSON), &pipeline); err != nil {
		return nil, nil, nil, fmt.Errorf("invalid pipeline JSON: %w", err)
	}

	currentState, ok := pipeline.States[inst.CurrentState]
	if !ok {
		return nil, nil, nil, fmt.Errorf("current state %q not defined in pipeline", inst.CurrentState)
	}

	transition, ok := currentState.Transitions[event]
	if !ok {
		return nil, nil, nil, fmt.Errorf("event %q is invalid from state %q", event, inst.CurrentState)
	}

	if transition.GuardRole != "" {
		allowed := false
		for _, r := range operatorRoles {
			if r == transition.GuardRole || r == "ADMIN" {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, nil, nil, errors.New("unauthorized role for transition")
		}
	}

	return inst, &pipeline, &transition, nil
}

// CheckTransition is a read-only peek at what event would do to instanceID
// from its current state — nothing is mutated or persisted. Used by
// pkg/telegram's callback handler to decide whether to prompt for a
// required note/manual fields (see TransitionDef.RequireNote/ManualFields)
// before ever calling DispatchEvent.
func (e *Engine) CheckTransition(ctx context.Context, instanceID int64, event string, operatorRoles []string) (*TransitionDef, error) {
	_, _, transition, err := e.resolveTransition(ctx, instanceID, event, operatorRoles)
	return transition, err
}

// DispatchEvent transitions instance from its current state upon event.
func (e *Engine) DispatchEvent(ctx context.Context, instanceID int64, event string, operatorName string, operatorRoles []string, operatorTgID int64, note string, extraVars map[string]string) (*model.WorkflowInstance, error) {
	inst, pipeline, transition, err := e.resolveTransition(ctx, instanceID, event, operatorRoles)
	if err != nil {
		return nil, err
	}

	if transition.RequireNote && strings.TrimSpace(note) == "" {
		return nil, ErrNoteRequired
	}
	for _, f := range transition.ManualFields {
		if f.Required && strings.TrimSpace(extraVars[f.Key]) == "" {
			return nil, fmt.Errorf("%w: %s", ErrManualFieldRequired, f.Key)
		}
	}

	targetStateName := transition.TargetState
	targetState, ok := pipeline.States[targetStateName]
	if !ok {
		return nil, fmt.Errorf("target state %q not defined in pipeline", targetStateName)
	}

	// Update context variables
	var currentVars map[string]string
	if err := json.Unmarshal([]byte(inst.ContextDataJSON), &currentVars); err != nil || currentVars == nil {
		currentVars = make(map[string]string)
	}
	for k, v := range extraVars {
		currentVars[k] = v
	}
	currentVars["operator"] = operatorName
	currentVars["last_event"] = event
	currentVars["note"] = note

	// Append history
	var history []HistoryEntry
	_ = json.Unmarshal([]byte(inst.HistoryJSON), &history)
	history = append(history, HistoryEntry{
		FromState: inst.CurrentState,
		ToState:   targetStateName,
		Event:     event,
		Operator:  operatorName,
		Note:      note,
		Timestamp: time.Now(),
	})
	histBytes, _ := json.Marshal(history)

	status := "running"
	var finishedAt *time.Time
	if targetState.IsTerminal {
		if strings.Contains(targetStateName, "reject") || strings.Contains(targetStateName, "fail") {
			status = "rejected"
		} else {
			status = "completed"
		}
		now := time.Now()
		finishedAt = &now
	}

	// Run auto output actions for the target state (and pipeline-level ones
	// if terminal) BEFORE persisting the transition. inst is updated in
	// memory first so handlers see the state they are transitioning into,
	// but nothing is written to the DB until every handler succeeds — a
	// failing handler aborts the whole dispatch so the
	// instance is never left claiming a state it didn't actually reach.
	originalState := inst.CurrentState
	inst.CurrentState = targetStateName
	inst.Status = status
	inst.FinishedAt = finishedAt

	if err := e.executeOutputActions(ctx, inst, targetState.OutputActions, currentVars); err != nil {
		return nil, err
	}
	if targetState.IsTerminal && len(pipeline.OutputActions) > 0 {
		if err := e.executeOutputActions(ctx, inst, pipeline.OutputActions, currentVars); err != nil {
			return nil, err
		}
	}

	ctxBytes, _ := json.Marshal(currentVars)

	if err := e.store.UpdateInstanceState(ctx, inst.ID, originalState, targetStateName, status, string(histBytes), string(ctxBytes), finishedAt); err != nil {
		return nil, err
	}

	inst.ContextDataJSON = string(ctxBytes)
	inst.HistoryJSON = string(histBytes)

	// Execute entry actions of target state
	e.executeActions(ctx, inst, targetState.EntryActions, pipeline)

	return inst, nil
}

func (e *Engine) executeOutputActions(ctx context.Context, inst *model.WorkflowInstance, actions []OutputActionDef, vars map[string]string) error {
	for _, action := range actions {
		if action.ExecutionMode == "manual" {
			// Manual actions are triggered by operators via ExecuteManualAction
			continue
		}
		handler, ok := e.outputHandlers[action.Type]
		if !ok {
			continue
		}
		if err := handler(ctx, inst, action, vars); err != nil {
			logger.Error("Workflow output action failed", logger.String("type", action.Type), logger.String("name", action.Name), logger.Any("err", err))
			return fmt.Errorf("output action %q (%s) failed: %w", action.Name, action.Type, err)
		}
	}
	return nil
}

// ExecuteManualAction allows an authorized operator to trigger a manual output action.
func (e *Engine) ExecuteManualAction(ctx context.Context, instanceID int64, actionType string, operatorName string, payload map[string]string) (*model.WorkflowInstance, error) {
	inst, err := e.store.GetInstance(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("instance %d not found: %w", instanceID, err)
	}

	handler, ok := e.outputHandlers[actionType]
	if !ok {
		return nil, fmt.Errorf("no output action handler registered for %q", actionType)
	}

	action := OutputActionDef{
		Type:          actionType,
		ExecutionMode: "manual",
		Params:        payload,
	}

	var ctxVars map[string]string
	_ = json.Unmarshal([]byte(inst.ContextDataJSON), &ctxVars)
	if ctxVars == nil {
		ctxVars = make(map[string]string)
	}
	for k, v := range payload {
		ctxVars[k] = v
	}

	if err := handler(ctx, inst, action, ctxVars); err != nil {
		return nil, fmt.Errorf("manual action execution failed: %w", err)
	}

	// Record in history
	var history []HistoryEntry
	_ = json.Unmarshal([]byte(inst.HistoryJSON), &history)
	history = append(history, HistoryEntry{
		FromState: inst.CurrentState,
		ToState:   inst.CurrentState,
		Event:     "manual_action:" + actionType,
		Operator:  operatorName,
		Note:      "手动执行操作: " + actionType,
		Timestamp: time.Now(),
	})
	histBytes, _ := json.Marshal(history)
	ctxBytes, _ := json.Marshal(ctxVars)

	if err := e.store.UpdateInstanceState(ctx, inst.ID, inst.CurrentState, inst.CurrentState, inst.Status, string(histBytes), string(ctxBytes), inst.FinishedAt); err != nil {
		return nil, err
	}
	inst.HistoryJSON = string(histBytes)
	inst.ContextDataJSON = string(ctxBytes)

	return inst, nil
}

func (e *Engine) executeActions(ctx context.Context, inst *model.WorkflowInstance, actions []ActionDef, pipeline *Pipeline) {
	for _, action := range actions {
		switch action.Type {
		case "notify":
			e.executeNotifyAction(ctx, inst, action)
		case "side_effect":
			if handler, ok := e.sideEffects[action.Handler]; ok {
				if err := handler(ctx, inst); err != nil {
					logger.Error("Workflow side effect failed", logger.String("handler", action.Handler), logger.Any("err", err))
				}
			}
		}
	}
}

func (e *Engine) executeNotifyAction(ctx context.Context, inst *model.WorkflowInstance, action ActionDef) {
	var ctxVars map[string]string
	_ = json.Unmarshal([]byte(inst.ContextDataJSON), &ctxVars)
	if ctxVars == nil {
		ctxVars = make(map[string]string)
	}
	// Merge action vars
	for k, v := range action.Vars {
		ctxVars[k] = v
	}
	ctxVars["request_id"] = strconv.FormatInt(inst.BusinessID, 10)
	ctxVars["instance_id"] = strconv.FormatInt(inst.ID, 10)
	ctxVars["requester"] = inst.RequesterName

	// Resolve recipients
	recipients := e.resolveRecipients(ctx, action.Recipient, inst)
	if len(recipients) == 0 {
		return
	}

	// Render content
	var titleTpl, bodyTpl, buttonsJSON string
	if action.TemplateKey != "" && e.templates != nil {
		if tmpl, err := e.templates.GetByKey(ctx, action.TemplateKey); err == nil && tmpl != nil && tmpl.Enabled {
			titleTpl = tmpl.TitleTemplate
			bodyTpl = tmpl.BodyTemplate
			buttonsJSON = tmpl.ButtonsJSON
		}
	}
	if bodyTpl == "" {
		bodyTpl = action.Text
	}

	_, renderedBody, renderedButtons, err := template.Render(template.Source{
		TitleTemplate: titleTpl,
		BodyTemplate:  bodyTpl,
		ButtonsJSON:   buttonsJSON,
	}, ctxVars)
	if err != nil {
		logger.Error("Failed to render workflow template", logger.String("key", action.TemplateKey), logger.Any("err", err))
		return
	}

	if e.chatDriver == nil {
		return
	}

	// Build action buttons if action defines custom buttons. Target is
	// passed through as-is (including an empty string, or a "#"-prefixed
	// relative path) — the ChatDriver implementation owns resolving those
	// against its own default app URL, not the engine.
	var buttons []Button
	if len(action.Buttons) > 0 {
		for _, b := range action.Buttons {
			lbl := b.Label
			if strings.Contains(lbl, "{{") {
				_, lbl, _, _ = template.Render(template.Source{BodyTemplate: lbl}, ctxVars)
			}
			switch b.ActionType {
			case "callback":
				callbackData := fmt.Sprintf("%s:%d", b.Event, inst.ID)
				buttons = append(buttons, Button{Label: lbl, ActionType: "callback", Target: callbackData})
			case "webapp":
				targetURL := b.TargetURL
				if strings.Contains(targetURL, "{{") {
					_, targetURL, _, _ = template.Render(template.Source{BodyTemplate: targetURL}, ctxVars)
				}
				buttons = append(buttons, Button{Label: lbl, ActionType: "webapp", Target: targetURL})
			case "url":
				targetURL := b.TargetURL
				if strings.Contains(targetURL, "{{") {
					_, targetURL, _, _ = template.Render(template.Source{BodyTemplate: targetURL}, ctxVars)
				}
				buttons = append(buttons, Button{Label: lbl, ActionType: "url", Target: targetURL})
			}
		}
	} else if len(renderedButtons) > 0 {
		buttons = ButtonsFromTemplate(template.BindCallbackEntity(renderedButtons, inst.ID))
	}

	for _, chatID := range recipients {
		if chatID == 0 {
			continue
		}
		_ = e.chatDriver.SendMessage(ctx, chatID, renderedBody, buttons)
	}
}

func (e *Engine) resolveRecipients(ctx context.Context, recipientExpr string, inst *model.WorkflowInstance) []int64 {
	trimmed := strings.TrimSpace(recipientExpr)
	if trimmed == "$.requester" {
		if inst.RequesterTelegramUserID != 0 {
			return []int64{inst.RequesterTelegramUserID}
		}
		return nil
	}

	if strings.HasPrefix(trimmed, "role:") && e.bindings != nil {
		role := strings.TrimPrefix(trimmed, "role:")
		bindings, err := e.bindings.ListActiveByRole(ctx, role)
		if err != nil {
			bindings = nil
		}
		admins, err := e.bindings.ListActiveAdmins(ctx)
		if err != nil {
			admins = nil
		}
		seen := make(map[int64]bool)
		res := make([]int64, 0, len(bindings)+len(admins))
		for _, b := range append(bindings, admins...) {
			if !seen[b.TelegramUserID] {
				seen[b.TelegramUserID] = true
				res = append(res, b.TelegramUserID)
			}
		}
		return res
	}

	if strings.HasPrefix(trimmed, "user:") {
		idStr := strings.TrimPrefix(trimmed, "user:")
		if id, err := strconv.ParseInt(idStr, 10, 64); err == nil {
			return []int64{id}
		}
	}

	return nil
}
