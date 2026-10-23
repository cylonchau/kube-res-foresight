package telegram

import (
	"context"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/config"
	"github.com/cylonchau/kubeforecast/pkg/logger"
	"github.com/cylonchau/kubeforecast/pkg/message"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/notify/template"
	"github.com/cylonchau/kubeforecast/pkg/workflow"
	tele "gopkg.in/telebot.v3"
	"html"
	"strconv"
	"strings"
	"time"
)

type BindingManager interface {
	ConfirmBindWithToken(context.Context, string, *TelegramUser, time.Time) (*model.TelegramBinding, error)
	GetBindingByTelegramID(context.Context, int64) (*model.TelegramBinding, error)
	ListActiveAdmins(context.Context) ([]model.TelegramBinding, error)
	ListActiveByRole(context.Context, string) ([]model.TelegramBinding, error)
}

type TemplateProvider interface {
	GetByKey(context.Context, string) (*model.NotifyTemplate, error)
}

type WorkflowRunner interface {
	HandleIncomingText(context.Context, workflow.ChatUser, string) (workflow.CommandReply, error)
	HandleIncomingAction(context.Context, workflow.ChatUser, string, int64) (workflow.ActionOutcome, error)
}

// BotDeps are the collaborators one bot needs. BotID scopes command matching
// to the commands of that bot; Sink (optional) receives a standard event for
// every command the bot is asked to run.
type BotDeps struct {
	BotID          int64
	BindingMgr     BindingManager
	Templates      TemplateProvider
	WorkflowEngine WorkflowRunner
	Sink           message.Sink
}

// BotService is the Bot API adapter for one bot. It is the only place the
// Telegram bot SDK is used; everything else talks to it through the Telegram
// Agent.
type BotService struct {
	bot        *tele.Bot
	deps       BotDeps
	miniAppURL string
}

// scope returns a context limited to this bot's commands.
func (s *BotService) scope() context.Context {
	ctx := context.Background()
	if s.deps.BotID > 0 {
		ctx = model.WithBot(ctx, s.deps.BotID)
	}
	return ctx
}

func NewBotService(cfg config.TelegramConfig, deps BotDeps) (*BotService, error) {
	if !cfg.Enabled || cfg.BotToken == "" {
		return nil, nil
	}
	bot, err := tele.NewBot(tele.Settings{Token: cfg.BotToken, URL: cfg.APIBaseURL, ParseMode: tele.ModeHTML, Poller: &tele.LongPoller{Timeout: 10 * time.Second}})
	if err != nil {
		return nil, fmt.Errorf("initialize telegram bot: %w", err)
	}
	service := &BotService{bot: bot, deps: deps, miniAppURL: cfg.MiniAppURL}
	service.registerHandlers()
	return service, nil
}

func (s *BotService) registerHandlers() {
	s.bot.Handle("/start", s.handleStart)
	s.bot.Handle(tele.OnCallback, s.handleCallback)
	s.bot.Handle(tele.OnText, s.handleText)
}

func (s *BotService) handleStart(c tele.Context) error {
	sender := c.Sender()
	if sender == nil {
		return nil
	}
	payload := strings.TrimSpace(c.Message().Payload)
	if strings.HasPrefix(payload, "bind_") && s.deps.BindingMgr != nil {
		binding, err := s.deps.BindingMgr.ConfirmBindWithToken(s.scope(), payload, &TelegramUser{ID: sender.ID, FirstName: sender.FirstName, LastName: sender.LastName, Username: sender.Username}, time.Now())
		if err != nil {
			return c.Send(s.renderTextOrFallback(model.TemplateKeyBindFailedMsg, map[string]string{"error": html.EscapeString(err.Error())}, "Binding failed"))
		}
		return c.Send(s.renderTextOrFallback(model.TemplateKeyBindingWelcome, map[string]string{"user_name": html.EscapeString(model.DisplayName(binding.User))}, "Welcome to KubeForecast"))
	}
	return c.Send(s.renderTextOrFallback(model.TemplateKeyStartWelcomeMsg, nil, "Welcome to KubeForecast"), s.defaultMenu())
}

func (s *BotService) renderTextOrFallback(key string, vars map[string]string, fallback string) string {
	if s.deps.Templates == nil {
		return fallback
	}
	tmpl, err := s.deps.Templates.GetByKey(context.Background(), key)
	if err != nil || tmpl == nil || !tmpl.Enabled {
		return fallback
	}
	_, body, _, err := template.Render(template.Source{TitleTemplate: tmpl.TitleTemplate, BodyTemplate: tmpl.BodyTemplate, ButtonsJSON: tmpl.ButtonsJSON}, vars)
	if err != nil {
		return fallback
	}
	return body
}

func (s *BotService) defaultMenu() *tele.ReplyMarkup {
	if s.miniAppURL == "" {
		return nil
	}
	menu := &tele.ReplyMarkup{}
	menu.Inline(menu.Row(menu.WebApp("Open KubeForecast", &tele.WebApp{URL: s.miniAppURL})))
	return menu
}

func (s *BotService) buildMenu(buttons []template.Button) *tele.ReplyMarkup {
	menu := &tele.ReplyMarkup{}
	rows := make([]tele.Row, 0, len(buttons))
	for _, button := range buttons {
		switch button.ActionType {
		case template.ActionTypeURL:
			rows = append(rows, tele.Row{tele.Btn{Text: button.Label, URL: button.ActionTarget}})
		case template.ActionTypeWebApp:
			target := button.ActionTarget
			if strings.HasPrefix(target, "#") {
				target = s.miniAppURL + target
			}
			rows = append(rows, tele.Row{tele.Btn{Text: button.Label, WebApp: &tele.WebApp{URL: target}}})
		default:
			rows = append(rows, tele.Row{tele.Btn{Text: button.Label, Data: button.ActionTarget}})
		}
	}
	menu.Inline(rows...)
	return menu
}

func chatUserOf(sender *tele.User) workflow.ChatUser {
	return workflow.ChatUser{PlatformUserID: sender.ID, Username: sender.Username, FirstName: sender.FirstName, LastName: sender.LastName}
}

func (s *BotService) handleCallback(c tele.Context) error {
	action, rawID, ok := strings.Cut(strings.TrimSpace(c.Data()), ":")
	if !ok || c.Sender() == nil || s.deps.WorkflowEngine == nil {
		return c.Respond()
	}
	instanceID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		return c.Respond()
	}
	outcome, runErr := s.deps.WorkflowEngine.HandleIncomingAction(s.scope(), chatUserOf(c.Sender()), action, instanceID)
	if runErr != nil {
		logger.Error("Failed to handle workflow action", logger.Err(runErr))
	}
	if outcome.PromptForNote {
		_ = c.Respond()
		prompt := outcome.NotePrompt
		if prompt == "" {
			prompt = "Please enter a note:"
		}
		return c.Send(prompt, &tele.ReplyMarkup{ForceReply: true})
	}
	return c.Respond(&tele.CallbackResponse{Text: outcome.Reply, ShowAlert: outcome.ShowAlert})
}

func (s *BotService) handleText(c tele.Context) error {
	if c.Sender() == nil || s.deps.WorkflowEngine == nil || strings.TrimSpace(c.Text()) == "" {
		return nil
	}
	text := strings.TrimSpace(c.Text())
	if s.deps.Sink != nil && strings.HasPrefix(text, "/") {
		s.deps.Sink.Handle(message.Event{Type: message.EventCommand, Message: commandMessage(s.deps.BotID, c.Sender(), c.Message().ID, text)})
	}
	reply, err := s.deps.WorkflowEngine.HandleIncomingText(s.scope(), chatUserOf(c.Sender()), text)
	if err != nil {
		logger.Error("Failed to handle Telegram text", logger.Err(err))
	}
	if reply.Text == "" {
		return nil
	}
	if len(reply.Buttons) == 0 {
		return c.Send(reply.Text)
	}
	buttons := make([]template.Button, 0, len(reply.Buttons))
	for _, button := range reply.Buttons {
		buttons = append(buttons, template.Button{Label: button.Label, ActionType: button.ActionType, ActionTarget: button.Target})
	}
	return c.Send(reply.Text, s.buildMenu(buttons))
}

// Identity returns the bot's username and Telegram user id, as reported by
// getMe when the bot was created.
func (s *BotService) Identity() (username string, id int64) {
	if s.bot.Me == nil {
		return "", 0
	}
	return s.bot.Me.Username, s.bot.Me.ID
}

// CommandSpec is one entry of the command menu shown in Telegram.
type CommandSpec struct{ Command, Description string }

// SyncCommands pushes the command menu to Telegram through the Bot API.
func (s *BotService) SyncCommands(specs []CommandSpec) error {
	commands := make([]tele.Command, 0, len(specs))
	for _, spec := range specs {
		name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(spec.Command), "/"))
		if name == "" || strings.ContainsAny(name, " \t") {
			continue
		}
		description := strings.TrimSpace(spec.Description)
		if description == "" {
			description = name
		}
		commands = append(commands, tele.Command{Text: name, Description: description})
	}
	return s.bot.SetCommands(commands)
}

// Start begins long polling and returns immediately; polling stops when ctx
// is cancelled.
func (s *BotService) Start(ctx context.Context) {
	if err := s.bot.RemoveWebhook(true); err != nil {
		logger.Warn("Failed to remove Telegram webhook", logger.Err(err))
	}
	go func() { <-ctx.Done(); s.bot.Stop() }()
	go s.bot.Start()
}

// commandMessage converts a command update into the standard message.
func commandMessage(botID int64, sender *tele.User, messageID int, text string) message.Message {
	name := sender.Username
	if name == "" {
		name = strings.TrimSpace(sender.FirstName + " " + sender.LastName)
	}
	return message.Message{
		Platform:   "telegram",
		Account:    strconv.FormatInt(botID, 10),
		Source:     strconv.FormatInt(sender.ID, 10),
		SourceType: message.SourceBot,
		MessageID:  strconv.Itoa(messageID),
		Sender:     name,
		Text:       text,
		Timestamp:  time.Now(),
		Metadata:   map[string]string{"sender_id": strconv.FormatInt(sender.ID, 10)},
	}
}
