package telegram

import (
	"context"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/notify/template"
	"github.com/cylonchau/kubeforecast/pkg/workflow"
	"html"
	"time"
)

func RegisterCommandHandlers(engine *workflow.Engine, bindMgr BindingManager, templates workflow.TemplateResolver) {
	engine.RegisterCommandHandler("confirm_bind", confirmBindCommandHandler(bindMgr, templates))
}

func confirmBindCommandHandler(bindMgr BindingManager, templates workflow.TemplateResolver) workflow.CommandHandler {
	return func(ctx context.Context, vars map[string]string, requesterUserID *uint, requesterTgID int64, requesterName string) (workflow.CommandReply, error) {
		token := vars["payload"]
		if token == "" {
			token = vars["param1"]
		}
		if token == "" {
			return workflow.CommandReply{}, fmt.Errorf("no bind token found")
		}
		binding, err := bindMgr.ConfirmBindWithToken(ctx, token, &TelegramUser{ID: requesterTgID, FirstName: vars["_sender_first_name"], LastName: vars["_sender_last_name"], Username: vars["_sender_username"]}, time.Now())
		if err != nil {
			return workflow.CommandReply{}, err
		}
		userName := html.EscapeString(model.DisplayName(binding.User))
		if templates != nil {
			if tmpl, err := templates.GetByKey(ctx, model.TemplateKeyBindConfirmSuccessMsg); err == nil && tmpl != nil && tmpl.Enabled {
				_, body, _, renderErr := template.Render(template.Source{TitleTemplate: tmpl.TitleTemplate, BodyTemplate: tmpl.BodyTemplate, ButtonsJSON: tmpl.ButtonsJSON}, map[string]string{"user_name": userName})
				if renderErr == nil {
					return workflow.CommandReply{Text: body}, nil
				}
			}
		}
		return workflow.CommandReply{Text: fmt.Sprintf("Binding successful: %s", userName)}, nil
	}
}
