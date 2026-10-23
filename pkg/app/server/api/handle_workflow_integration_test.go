package api

import (
	"context"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/workflow"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net/http"
	"testing"
)

func workflowAPIDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Role{}, &model.Permission{}, &model.TelegramBinding{}, &model.BotCommandDef{}, &model.WorkflowDef{}, &model.WorkflowInstance{}))
	return db
}

func workflowAPIRouter(t *testing.T) (*gin.Engine, *gorm.DB, *workflow.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := workflowAPIDB(t)
	adminRole := model.Role{Name: "Admin", Code: "ADMIN", Status: true}
	require.NoError(t, db.Create(&adminRole).Error)
	user := model.User{Username: "admin", Status: true, Roles: []model.Role{adminRole}}
	require.NoError(t, db.Create(&user).Error)
	engine := workflow.NewEngine(rdb.NewWorkflowStore(db), nil, nil, nil)
	engine.RegisterCommandHandler("echo", func(context.Context, map[string]string, *uint, int64, string) (workflow.CommandReply, error) {
		return workflow.CommandReply{Text: "ok"}, nil
	})
	engine.RegisterOutputActionHandler("record", func(context.Context, *model.WorkflowInstance, workflow.OutputActionDef, map[string]string) error {
		return nil
	})
	engine.RegisterOptionSource("assets", func(context.Context) ([]workflow.SelectOption, error) {
		return []workflow.SelectOption{{Label: "Bitcoin", Value: "BTC"}}, nil
	})
	handler := &WorkflowRouter{CommandDAO: rdb.NewBotCommandDAO(db), WorkflowDAO: rdb.NewWorkflowDefDAO(db), InstanceDAO: rdb.NewWorkflowInstanceDAO(db), Engine: engine, UserDAO: rdb.NewUserDAO(db)}
	router := gin.New()
	group := router.Group("/telegram")
	group.Use(func(c *gin.Context) { c.Set("roles", []string{"ADMIN"}); c.Set("userId", user.ID); c.Next() })
	handler.RegisterRoutes(group)
	return router, db, engine
}

func workflowPayload(key string, enabled bool) map[string]any {
	return map[string]any{
		"workflow_key": key, "name": "Approval", "driver": "telegram", "description": "test", "initial_state": "pending", "enabled": enabled,
		"pipeline_json": `{"initial_state":"pending","states":{"pending":{"transitions":{"approve":{"target_state":"approved","guard_role":"ADMIN"}}},"approved":{"is_terminal":true}}}`,
	}
}

func TestWorkflowAdminAPIEndToEnd(t *testing.T) {
	router, db, engine := workflowAPIRouter(t)

	rec := performJSON(t, router, http.MethodPost, "/telegram/admin/workflows", "", workflowPayload("approval", false))
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var def model.WorkflowDef
	require.NoError(t, db.Where("workflow_key = ?", "approval").First(&def).Error)
	assert.False(t, def.Enabled)

	rec = performJSON(t, router, http.MethodPut, "/telegram/admin/workflows/approval", "", workflowPayload("approval", true))
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodGet, "/telegram/admin/workflows?keyword=Approval&page=1&page_size=10", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodGet, "/telegram/admin/workflows/approval", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	command := map[string]any{"command": "/approve", "name": "Approve", "action_type": "workflow", "workflow_key": "approval", "enabled": true, "required_params_json": `{}`, "related_template_keys_json": `[]`}
	rec = performJSON(t, router, http.MethodPost, "/telegram/admin/commands", "", command)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var cmd model.BotCommandDef
	require.NoError(t, db.Where("command = ?", "/approve").First(&cmd).Error)
	command["name"] = "Approve Updated"
	rec = performJSON(t, router, http.MethodPut, fmt.Sprintf("/telegram/admin/commands/%d", cmd.ID), "", command)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodGet, "/telegram/admin/commands?keyword=approve", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = performJSON(t, router, http.MethodGet, "/telegram/admin/workflow-handlers", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "echo")
	rec = performJSON(t, router, http.MethodGet, "/telegram/admin/workflow-option-sources/assets", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "BTC")

	inst, err := engine.StartWorkflow(context.Background(), "approval", "request", 11, nil, 0, "admin", map[string]string{"asset": "BTC"})
	require.NoError(t, err)
	rec = performJSON(t, router, http.MethodGet, "/telegram/admin/workflow-instances?workflow_key=approval&status=running&requester_name=adm", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodGet, fmt.Sprintf("/telegram/admin/workflow-instances/%d", inst.ID), "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodPost, fmt.Sprintf("/telegram/admin/workflow-instances/%d/action", inst.ID), "", map[string]any{"action_type": "record", "payload": map[string]string{"value": "1"}})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodPost, fmt.Sprintf("/telegram/admin/workflow-instances/%d/transition", inst.ID), "", map[string]any{"event": "approve"})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	extra := model.WorkflowInstance{WorkflowKey: "approval", CurrentState: "pending", Status: "running", BusinessType: "request", BusinessID: 12}
	require.NoError(t, db.Create(&extra).Error)
	rec = performJSON(t, router, http.MethodPost, "/telegram/admin/workflow-instances/batch-delete", "", map[string]any{"ids": []int64{extra.ID}})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = performJSON(t, router, http.MethodPost, "/telegram/admin/commands/batch-delete", "", map[string]any{"ids": []int64{cmd.ID}})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodPost, "/telegram/admin/workflows/batch-delete", "", map[string]any{"keys": []string{"approval"}})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestWorkflowAdminAPIValidationAndAuthorization(t *testing.T) {
	router, _, _ := workflowAPIRouter(t)
	cases := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/telegram/admin/workflows", map[string]any{"workflow_key": "bad", "name": "Bad", "initial_state": "x", "pipeline_json": "not-json"}},
		{http.MethodPut, "/telegram/admin/workflows/missing", workflowPayload("missing", true)},
		{http.MethodPost, "/telegram/admin/commands", map[string]any{"command": "/bad", "name": "Bad", "action_type": "workflow"}},
		{http.MethodPost, "/telegram/admin/commands", map[string]any{"command": "/bad", "name": "Bad", "action_type": "handler", "params_json": `{"handler_name":"missing"}`}},
		{http.MethodGet, "/telegram/admin/workflow-option-sources/missing", nil},
		{http.MethodGet, "/telegram/admin/workflow-instances/not-a-number", nil},
	}
	for _, tc := range cases {
		rec := performJSON(t, router, tc.method, tc.path, "", tc.body)
		assert.GreaterOrEqual(t, rec.Code, 400, "%s %s body=%s", tc.method, tc.path, rec.Body.String())
	}

	gin.SetMode(gin.TestMode)
	denied := gin.New()
	group := denied.Group("/telegram")
	group.Use(func(c *gin.Context) { c.Set("roles", []string{"VIEWER"}); c.Next() })
	(&WorkflowRouter{}).RegisterRoutes(group)
	rec := performJSON(t, denied, http.MethodGet, "/telegram/admin/workflows", "", nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
