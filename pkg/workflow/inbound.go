package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/logger"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/notify/template"
	"strings"
)

// defaultNotePrompt is shown when a RequireNote transition doesn't declare
// its own NotePrompt.
const defaultNotePrompt = "请输入原因："

// HandleIncomingText is THE platform-agnostic entrypoint for "this chat
// user sent this text" — every chat driver (pkg/telegram today, any future
// platform) calls this and only this. The whole dispatch pipeline (consume
// a pending reply if one exists; otherwise MatchCommand → binding/role
// checks → ExecuteCommand) lives here once, not once per platform, so
// adding a new chat driver never means re-implementing this logic.
// Returns the CommandReply to send back (a zero value means nothing to
// send — e.g. a "workflow" command's notifications already went out via its
// pipeline's own entry_actions/ChatDriver).
func (e *Engine) HandleIncomingText(ctx context.Context, sender ChatUser, text string) (CommandReply, error) {
	if e.pendingReplies != nil {
		if instanceID, event, ok, err := e.pendingReplies.Consume(ctx, sender.PlatformUserID); err == nil && ok {
			reply, err := e.completePendingReply(ctx, sender, instanceID, event, text)
			return CommandReply{Text: reply}, err
		}
	}

	cmd, vars, err := e.MatchCommand(ctx, text)
	if err != nil || cmd == nil {
		return CommandReply{}, err
	}
	if vars == nil {
		vars = map[string]string{}
	}
	// Raw platform identity, for handler-type commands that need more than
	// a display name — e.g. confirm_bind must construct the driver's own
	// user type to create the binding row itself, and can't rely on one
	// already existing (that's the whole point of that command).
	vars["_sender_username"] = sender.Username
	vars["_sender_first_name"] = sender.FirstName
	vars["_sender_last_name"] = sender.LastName

	// Binding and role checks are independent: an unbound caller has no
	// roles by definition (so a role-gated command always denies them
	// regardless of RequireBinding), but a command whose whole purpose is
	// to create the binding (RequireBinding == false) must still be
	// reachable while unbound.
	var boundUser *model.User
	var requesterUserID *uint
	requesterName := sender.Username
	if requesterName == "" {
		requesterName = sender.FirstName
	}
	if e.bindings != nil {
		if binding, err := e.bindings.GetBindingByTelegramID(ctx, sender.PlatformUserID); err == nil && binding.User != nil {
			boundUser = binding.User
			requesterUserID = &binding.User.ID
			// One canonical identity format for everyone in a workflow —
			// "account (telegram)" — see model.ActorName.
			requesterName = model.ActorName(binding.User, binding)
		}
	}

	if boundUser == nil && cmd.RequireBinding {
		return CommandReply{Text: e.renderTextOrFallback(ctx, model.TemplateKeyBindingRequired, nil, "请先绑定您的 KubeForecast 账号后再发起操作。")}, nil
	}

	if cmd.RequiredRolesJSON != "" {
		var requiredRoles []string
		_ = json.Unmarshal([]byte(cmd.RequiredRolesJSON), &requiredRoles)
		// No dedicated NotifyTemplate for "unauthorized for command X" exists
		// (it could be admin-customized with wording naming a specific
		// command) — sent as a plain string rather than through a
		// mismatched template key.
		if len(requiredRoles) > 0 && (boundUser == nil || !HasRole(boundUser, requiredRoles...)) {
			return CommandReply{Text: "⛔ 权限不足，无法执行该指令"}, nil
		}
	}

	if missing := requiredParamLabels(cmd, vars); len(missing) > 0 {
		// A precise, actionable reply instead of silently starting a
		// workflow instance with the field missing (or, worse, the generic
		// "指令执行失败" fallback below, which doesn't say what's actually
		// wrong) — see model.BotCommandDef.RequiredParamsJSON.
		return CommandReply{Text: fmt.Sprintf("⚠️ 指令 %s 缺少必填参数：%s，请附带后重试。", cmd.Command, strings.Join(missing, "、"))}, nil
	}

	reply, err := e.ExecuteCommand(ctx, cmd, vars, requesterUserID, sender.PlatformUserID, requesterName)
	if err != nil {
		logger.Error("Failed to execute bot command", logger.String("command", cmd.Command), logger.Any("err", err))
		return CommandReply{Text: e.renderTextOrFallback(ctx, model.TemplateKeyCommandFailed, nil, "指令执行失败，请稍后重试。")}, err
	}
	return reply, nil
}

// HandleIncomingAction is THE platform-agnostic entrypoint for "this chat
// user tapped this action/button" (event name + WorkflowInstance ID) —
// dispatches a guarded pipeline transition. Nothing here is specific to any
// one pipeline or event name. Who may act is enforced by the pipeline's own
// guard_role on the transition (DispatchEvent checks operatorRoles against
// it) — this only resolves the caller's active role codes and forwards
// them, so a backend admin can regrant any action to a new role, or wire up
// a brand-new ticket type's buttons, purely through the WorkflowDef.
//
// Before dispatching, it peeks the transition via CheckTransition: if it
// declares ManualFields (a structured multi-field form), chat can't collect
// that inline, so the caller is pointed at the web admin/Mini App instead.
// If it declares RequireNote (e.g. a rejection reason), the outcome asks
// the driver to prompt for one (see ActionOutcome.PromptForNote) and defers
// the actual DispatchEvent until that reply arrives via HandleIncomingText.
func (e *Engine) HandleIncomingAction(ctx context.Context, sender ChatUser, action string, entityID int64) (ActionOutcome, error) {
	if e.bindings == nil {
		return ActionOutcome{}, nil
	}
	binding, err := e.bindings.GetBindingByTelegramID(ctx, sender.PlatformUserID)
	if err != nil || binding.User == nil {
		return ActionOutcome{
			Reply:     e.renderTextOrFallback(ctx, model.TemplateKeyActionUnauthorized, nil, "⛔ 无权限操作"),
			ShowAlert: true,
		}, nil
	}
	roleCodes := RoleCodesOf(binding.User)
	operatorName := telegramOperatorName(binding)

	if transition, err := e.CheckTransition(ctx, entityID, action, roleCodes); err == nil {
		if len(transition.ManualFields) > 0 {
			return ActionOutcome{Reply: "该操作需要在管理后台/小程序内填写更多信息，请前往处理。", ShowAlert: true}, nil
		}
		if transition.RequireNote {
			prompt := transition.NotePrompt
			if prompt == "" {
				prompt = defaultNotePrompt
			}
			if e.pendingReplies != nil {
				_ = e.pendingReplies.Begin(ctx, sender.PlatformUserID, entityID, action)
			}
			return ActionOutcome{PromptForNote: true, NotePrompt: prompt}, nil
		}
	}

	if _, err := e.DispatchEvent(ctx, entityID, action, operatorName, roleCodes, sender.PlatformUserID, "", nil); err != nil {
		return ActionOutcome{
			Reply:     e.renderTextOrFallback(ctx, model.TemplateKeyActionFailed, nil, "操作失败：该工单可能已被处理，或您没有权限执行此操作"),
			ShowAlert: true,
		}, nil
	}

	switch action {
	case "reject":
		return ActionOutcome{Reply: e.renderTextOrFallback(ctx, model.TemplateKeyActionRejected, nil, "已拒绝")}, nil
	case "approve":
		return ActionOutcome{Reply: e.renderTextOrFallback(ctx, model.TemplateKeyActionApproved, nil, "✅ 已批准")}, nil
	default:
		return ActionOutcome{Reply: "✅ 已处理"}, nil
	}
}

// completePendingReply fires the DispatchEvent a RequireNote prompt was
// deferring: sender's next text message (note) is the transition's
// required note. Roles are re-resolved fresh here rather than carried over
// from the original button tap, so a role change in between is honored.
func (e *Engine) completePendingReply(ctx context.Context, sender ChatUser, instanceID int64, event, note string) (string, error) {
	if e.bindings == nil {
		return "", nil
	}
	binding, err := e.bindings.GetBindingByTelegramID(ctx, sender.PlatformUserID)
	if err != nil || binding.User == nil {
		return "⛔ 无法确认您的身份，请重新操作。", nil
	}
	operatorName := telegramOperatorName(binding)
	if _, err := e.DispatchEvent(ctx, instanceID, event, operatorName, RoleCodesOf(binding.User), sender.PlatformUserID, note, nil); err != nil {
		return fmt.Sprintf("❌ 操作失败：%s", err.Error()), err
	}
	return "✅ 已记录并处理。", nil
}

// renderTextOrFallback loads and renders a NotifyTemplate's body (no
// buttons — button construction is driver-specific UI, not this engine's
// concern), falling back to fallbackText if the template can't be
// loaded/is disabled/fails to render, so callers keep working even before
// migration seeds run.
func (e *Engine) renderTextOrFallback(ctx context.Context, key string, vars map[string]string, fallbackText string) string {
	if e.templates != nil {
		if tmpl, err := e.templates.GetByKey(ctx, key); err == nil && tmpl != nil && tmpl.Enabled {
			_, body, _, err := template.Render(template.Source{BodyTemplate: tmpl.BodyTemplate}, vars)
			if err == nil && body != "" {
				return body
			}
		}
	}
	return fallbackText
}

// telegramOperatorName resolves the identity to record for a Telegram-
// triggered approve/reject/note. It is model.ActorName — the same
// "account (telegram)" format the web console records for a web-triggered
// action — so an audit trail doesn't name the same person two different ways
// depending on which surface they happened to act from.
func telegramOperatorName(binding *model.TelegramBinding) string {
	return model.ActorName(binding.User, binding)
}

// RoleCodesOf extracts a user's active role codes.
func RoleCodesOf(user *model.User) []string {
	codes := make([]string, 0, len(user.Roles))
	for _, r := range user.Roles {
		if r.Status {
			codes = append(codes, r.Code)
		}
	}
	return codes
}

// HasRole reports whether user holds an active role matching any of the
// given codes. ADMIN is NOT implicitly included — callers that want
// "APPROVER-or-ADMIN"/"OPS-or-ADMIN" semantics pass ADMIN explicitly, so a
// single-admin setup still works end-to-end without assigning the new
// APPROVER/OPS roles.
func HasRole(user *model.User, codes ...string) bool {
	if user == nil {
		return false
	}
	want := make(map[string]bool, len(codes))
	for _, code := range codes {
		want[code] = true
	}
	for _, role := range user.Roles {
		if role.Status && want[role.Code] {
			return true
		}
	}
	return false
}
