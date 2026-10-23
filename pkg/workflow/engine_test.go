package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"
)

type engineStore struct {
	defs      map[string]*model.WorkflowDef
	commands  []model.BotCommandDef
	instances map[int64]*model.WorkflowInstance
	nextID    int64
	updateErr error
}

func (s *engineStore) GetWorkflowDef(_ context.Context, key string) (*model.WorkflowDef, error) {
	if item := s.defs[key]; item != nil {
		return item, nil
	}
	return nil, errors.New("not found")
}
func (s *engineStore) GetCommand(_ context.Context, cmd string) (*model.BotCommandDef, error) {
	for i := range s.commands {
		if s.commands[i].Command == cmd {
			return &s.commands[i], nil
		}
	}
	return nil, errors.New("not found")
}
func (s *engineStore) ListCommands(context.Context) ([]model.BotCommandDef, error) {
	return s.commands, nil
}
func (s *engineStore) CreateInstance(_ context.Context, inst *model.WorkflowInstance) error {
	s.nextID++
	inst.ID = s.nextID
	clone := *inst
	s.instances[inst.ID] = &clone
	return nil
}
func (s *engineStore) GetInstance(_ context.Context, id int64) (*model.WorkflowInstance, error) {
	if item := s.instances[id]; item != nil {
		clone := *item
		return &clone, nil
	}
	return nil, errors.New("not found")
}
func (s *engineStore) GetInstanceByBusiness(_ context.Context, kind string, id int64) (*model.WorkflowInstance, error) {
	for _, item := range s.instances {
		if item.BusinessType == kind && item.BusinessID == id {
			return item, nil
		}
	}
	return nil, errors.New("not found")
}
func (s *engineStore) UpdateInstanceState(_ context.Context, id int64, from, state, status, history, data string, finished *time.Time) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	item := s.instances[id]
	if item == nil || item.CurrentState != from {
		return errors.New("state changed")
	}
	item.CurrentState, item.Status, item.HistoryJSON, item.ContextDataJSON, item.FinishedAt = state, status, history, data, finished
	return nil
}

type engineTemplates map[string]*model.NotifyTemplate

func (t engineTemplates) GetByKey(_ context.Context, key string) (*model.NotifyTemplate, error) {
	if item := t[key]; item != nil {
		return item, nil
	}
	return nil, errors.New("not found")
}

type engineBindings struct {
	binding      *model.TelegramBinding
	role, admins []model.TelegramBinding
}

func (b engineBindings) GetBindingByTelegramID(context.Context, int64) (*model.TelegramBinding, error) {
	if b.binding == nil {
		return nil, errors.New("not found")
	}
	return b.binding, nil
}
func (b engineBindings) ListActiveByRole(context.Context, string) ([]model.TelegramBinding, error) {
	return b.role, nil
}
func (b engineBindings) ListActiveAdmins(context.Context) ([]model.TelegramBinding, error) {
	return b.admins, nil
}

type sentMessage struct {
	recipient int64
	text      string
	buttons   []Button
}
type engineChat struct{ messages []sentMessage }

func (c *engineChat) SendMessage(_ context.Context, id int64, text string, buttons []Button) error {
	c.messages = append(c.messages, sentMessage{id, text, buttons})
	return nil
}

type enginePending struct {
	id, user int64
	event    string
	active   bool
}

func (p *enginePending) Begin(_ context.Context, user, id int64, event string) error {
	p.user, p.id, p.event, p.active = user, id, event, true
	return nil
}
func (p *enginePending) Consume(_ context.Context, user int64) (int64, string, bool, error) {
	if !p.active || p.user != user {
		return 0, "", false, nil
	}
	p.active = false
	return p.id, p.event, true, nil
}
func (p *enginePending) Clear(context.Context, int64) error { p.active = false; return nil }

func testPipeline(t *testing.T) string {
	t.Helper()
	p := Pipeline{InitialState: "pending", States: map[string]StateDef{
		"pending": {Transitions: map[string]TransitionDef{
			"approve": {TargetState: "approved", GuardRole: "APPROVER"},
			"reject":  {TargetState: "rejected", GuardRole: "APPROVER", RequireNote: true},
			"assign":  {TargetState: "approved", GuardRole: "OPS", ManualFields: []ManualFieldDef{{Key: "owner", Required: true}}},
		}},
		"approved": {IsTerminal: true, EntryActions: []ActionDef{{Type: "notify", Recipient: "$.requester", Text: "approved {{.requester}}"}}},
		"rejected": {IsTerminal: true},
	}}
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	return string(raw)
}

func newEngineFixture(t *testing.T) (*Engine, *engineStore, *engineChat) {
	t.Helper()
	store := &engineStore{defs: map[string]*model.WorkflowDef{"ticket": {WorkflowKey: "ticket", Name: "Ticket", InitialState: "pending", PipelineJSON: testPipeline(t), Enabled: true}}, instances: map[int64]*model.WorkflowInstance{}}
	chat := &engineChat{}
	return NewEngine(store, engineTemplates{}, nil, chat), store, chat
}

func TestEngineWorkflowLifecycle(t *testing.T) {
	e, store, chat := newEngineFixture(t)
	called := 0
	e.RegisterSideEffect("audit", func(context.Context, *model.WorkflowInstance) error { called++; return nil })

	inst, err := e.StartWorkflow(context.Background(), "ticket", "request", 42, nil, 99, "alice", map[string]string{"asset": "BTC"})
	require.NoError(t, err)
	assert.Equal(t, "pending", inst.CurrentState)

	_, err = e.DispatchEvent(context.Background(), inst.ID, "approve", "operator", []string{"VIEWER"}, 1, "", nil)
	assert.ErrorContains(t, err, "unauthorized")
	_, err = e.DispatchEvent(context.Background(), inst.ID, "assign", "operator", []string{"OPS"}, 1, "", nil)
	assert.ErrorIs(t, err, ErrManualFieldRequired)

	done, err := e.DispatchEvent(context.Background(), inst.ID, "approve", "operator", []string{"APPROVER"}, 1, "", map[string]string{"result": "ok"})
	require.NoError(t, err)
	assert.Equal(t, "completed", done.Status)
	require.Len(t, chat.messages, 1)
	assert.Equal(t, int64(99), chat.messages[0].recipient)
	assert.Contains(t, chat.messages[0].text, "alice")
	assert.NotNil(t, store.instances[inst.ID].FinishedAt)

	store.defs["off"] = &model.WorkflowDef{WorkflowKey: "off", Enabled: false}
	_, err = e.StartWorkflow(context.Background(), "off", "", 0, nil, 0, "", nil)
	assert.ErrorContains(t, err, "disabled")
	_, err = e.StartWorkflow(context.Background(), "missing", "", 0, nil, 0, "", nil)
	assert.ErrorContains(t, err, "not found")
	assert.Zero(t, called)
}

func TestEngineCommandsOptionsAndWebhook(t *testing.T) {
	e, store, _ := newEngineFixture(t)
	e.RegisterCommandHandler("echo", func(_ context.Context, vars map[string]string, _ *uint, _ int64, name string) (CommandReply, error) {
		return CommandReply{Text: name + ":" + vars["payload"]}, nil
	})
	e.RegisterOptionSource("assets", func(context.Context) ([]SelectOption, error) {
		return []SelectOption{{Label: "Bitcoin", Value: "BTC"}}, nil
	})
	assert.Equal(t, []string{"assets"}, e.RegisteredOptionSourceNames())
	options, err := e.ResolveOptionSource(context.Background(), "assets")
	require.NoError(t, err)
	assert.Equal(t, "BTC", options[0].Value)
	_, err = e.ResolveOptionSource(context.Background(), "missing")
	assert.Error(t, err)

	store.commands = []model.BotCommandDef{
		{Command: "/disabled", Enabled: false},
		{Command: "/run", Pattern: `^/run\s+(.+)$`, Enabled: true},
		{Command: "natural", Pattern: `^price (.+)$`, Enabled: true},
	}
	cmd, vars, err := e.MatchCommand(context.Background(), " /run BTC ")
	require.NoError(t, err)
	require.NotNil(t, cmd)
	assert.Equal(t, "BTC", vars["param1"])
	cmd, vars, err = e.MatchCommand(context.Background(), "price ETH")
	require.NoError(t, err)
	assert.Equal(t, "ETH", vars["param1"])

	reply, err := e.ExecuteCommand(context.Background(), &model.BotCommandDef{Command: "/echo", ActionType: CommandActionHandler, ParamsJSON: `{"handler_name":"echo"}`}, map[string]string{"payload": "ok"}, nil, 1, "bob")
	require.NoError(t, err)
	assert.Equal(t, "bob:ok", reply.Text)
	_, err = e.ExecuteCommand(context.Background(), &model.BotCommandDef{ActionType: CommandActionHandler, ParamsJSON: `{"handler_name":"none"}`}, nil, nil, 0, "")
	assert.Error(t, err)
	_, err = e.ExecuteCommand(context.Background(), &model.BotCommandDef{ActionType: "unknown"}, nil, nil, 0, "")
	assert.Error(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer token", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	reply, err = e.ExecuteCommand(context.Background(), &model.BotCommandDef{Command: "/hook", ActionType: CommandActionWebhook, ParamsJSON: `{"url":"` + server.URL + `","auth_header":"Bearer token","success_message":"sent {{.payload}}"}`}, map[string]string{"payload": "BTC"}, nil, 7, "bob")
	require.NoError(t, err)
	assert.Equal(t, "sent BTC", reply.Text)
	err = postWebhook(context.Background(), map[string]string{}, nil, nil)
	assert.Error(t, err)
}

func TestEngineIncomingActionsAndPendingReply(t *testing.T) {
	e, store, _ := newEngineFixture(t)
	user := &model.User{ID: 5, Username: "alice", Roles: []model.Role{{Code: "APPROVER", Status: true}, {Code: "OPS", Status: true}}}
	binding := &model.TelegramBinding{TelegramUserID: 88, TelegramUsername: "tg_alice", User: user}
	e.bindings = engineBindings{binding: binding}
	pending := &enginePending{}
	e.SetPendingReplyStore(pending)

	inst, err := e.StartWorkflow(context.Background(), "ticket", "request", 1, &user.ID, 88, "alice", nil)
	require.NoError(t, err)
	outcome, err := e.HandleIncomingAction(context.Background(), ChatUser{PlatformUserID: 88}, "reject", inst.ID)
	require.NoError(t, err)
	assert.True(t, outcome.PromptForNote)
	assert.True(t, pending.active)
	reply, err := e.HandleIncomingText(context.Background(), ChatUser{PlatformUserID: 88}, "not acceptable")
	require.NoError(t, err)
	assert.Contains(t, reply.Text, "已记录")
	assert.Equal(t, "rejected", store.instances[inst.ID].Status)

	inst, err = e.StartWorkflow(context.Background(), "ticket", "request", 2, &user.ID, 88, "alice", nil)
	require.NoError(t, err)
	outcome, err = e.HandleIncomingAction(context.Background(), ChatUser{PlatformUserID: 88}, "assign", inst.ID)
	require.NoError(t, err)
	assert.True(t, outcome.ShowAlert)
	assert.Contains(t, outcome.Reply, "管理后台")

	assert.True(t, HasRole(user, "APPROVER"))
	assert.True(t, HasRole(user, "OPS"))
	codes := RoleCodesOf(user)
	sort.Strings(codes)
	assert.Equal(t, []string{"APPROVER", "OPS"}, codes)
}
