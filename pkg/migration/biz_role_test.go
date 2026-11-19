package migration

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/casbin/casbin/v3/util"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// allowed says whether a role may call a route: the same pattern matching the permission check uses, over the routes of the permissions of the role.
func allowed(t *testing.T, db *gorm.DB, roleCode, method, path string) bool {
	t.Helper()
	var role model.Role
	require.NoError(t, db.Preload("Permissions").Where("code = ?", roleCode).First(&role).Error)
	for _, p := range role.Permissions {
		var specs []string
		if p.BeApis == "" {
			continue
		}
		require.NoError(t, json.Unmarshal([]byte(p.BeApis), &specs), p.Code)
		for _, spec := range specs {
			var m, pattern string
			for i := 0; i < len(spec); i++ {
				if spec[i] == ' ' {
					m, pattern = spec[:i], spec[i+1:]
					break
				}
			}
			if m == method && util.KeyMatch2(path, pattern) {
				return true
			}
		}
	}
	return false
}

func TestTheBusinessRoleHasThePagesOfTheForecastsAndTheChatAndNothingOfTheSystem(t *testing.T) {
	db := setupPlatformDB(t)
	var role model.Role
	require.NoError(t, db.Preload("Permissions").Where("code = ?", BusinessRoleCode).First(&role).Error)
	var codes []string
	for _, p := range role.Permissions {
		codes = append(codes, p.Code)
	}
	assert.ElementsMatch(t, businessPermissions, codes, "the role has the menus of module management, results, recommendations and execution, the chat and its button, and nothing else")

	yes := [][2]string{
		{"GET", "/api/v1/modules"}, {"POST", "/api/v1/modules"}, {"PUT", "/api/v1/modules/5"}, {"DELETE", "/api/v1/modules/5"},
		{"GET", "/api/v1/modules/5/compare"}, {"POST", "/api/v1/modules/5/steps/score"}, {"GET", "/api/v1/modules/5/effect-report"},
		{"GET", "/api/v1/evaluations"}, {"GET", "/api/v1/training-runs/summary"}, {"GET", "/api/v1/training-runs/7"}, {"GET", "/api/v1/algorithm-scores/latest"},
		{"GET", "/api/v1/recommendations"}, {"GET", "/api/v1/scaling-decisions"}, {"GET", "/api/v1/fallbacks"}, {"GET", "/api/v1/admissions"},
		{"GET", "/api/v1/resource-analysis"}, {"GET", "/api/v1/snapshots/9"},
		{"POST", "/api/v1/assistant/chat/async"}, {"GET", "/api/v1/assistant/async/3"}, {"GET", "/api/v1/assistant/async/3/events"}, {"POST", "/api/v1/assistant/async/3/cancel"},
		{"GET", "/api/v1/assistant/conversations"}, {"POST", "/api/v1/assistant/conversations"}, {"GET", "/api/v1/assistant/conversations/4"},
		{"PUT", "/api/v1/assistant/conversations/4"}, {"DELETE", "/api/v1/assistant/conversations/4"}, {"POST", "/api/v1/assistant/conversations/batch-delete"},
		{"GET", "/api/v1/assistant/status"}, {"POST", "/api/v1/assistant/skill"},
	}
	for _, c := range yes {
		assert.True(t, allowed(t, db, BusinessRoleCode, c[0], c[1]), "%s %s is a page or the chat of the role", c[0], c[1])
	}
	no := [][2]string{
		{"GET", "/api/v1/users"}, {"POST", "/api/v1/roles"}, {"GET", "/api/v1/permissions"}, {"PUT", "/api/v1/configs/assistant.enabled"}, {"GET", "/api/v1/secret-keys"},
		{"GET", "/api/v1/datasources"}, {"POST", "/api/v1/datasources"}, {"POST", "/api/v1/algorithms"}, {"PUT", "/api/v1/algorithms/3"},
		{"GET", "/api/v1/assistant/connections"}, {"POST", "/api/v1/assistant/connections"}, {"GET", "/api/v1/assistant/audit"}, {"GET", "/api/v1/assistant/audit/5"},
		{"GET", "/api/v1/assistant/tasks"}, {"GET", "/api/v1/assistant/skills"}, {"GET", "/api/v1/diagnostics/database"}, {"GET", "/api/v1/operation-logs"},
		{"DELETE", "/api/v1/operation-logs"}, {"GET", "/api/v1/jobs"}, {"POST", "/api/v1/records/batch-delete"}, {"GET", "/api/v1/dashboard"},
	}
	for _, c := range no {
		assert.False(t, allowed(t, db, BusinessRoleCode, c[0], c[1]), "%s %s is not for the business role", c[0], c[1])
	}
}

func TestTheBusinessRoleIsMadeOnceAndAnAdministratorsChangesToItStay(t *testing.T) {
	db := setupPlatformDB(t)
	var role model.Role
	require.NoError(t, db.Where("code = ?", BusinessRoleCode).First(&role).Error)
	require.NoError(t, db.Model(&role).Association("Permissions").Clear())
	require.NoError(t, seedBusinessRole(db))
	var left int64
	require.NoError(t, db.Table("sys_role_permissions").Where("role_id = ?", role.ID).Count(&left).Error)
	assert.Zero(t, left, "an administrator who took the permissions away is not given them back")
	var roles int64
	require.NoError(t, db.Model(&model.Role{}).Where("code = ?", BusinessRoleCode).Count(&roles).Error)
	assert.EqualValues(t, 1, roles)
}

func TestTheStarOfAPermissionNeedsASlashBeforeItToMatchAnythingBelow(t *testing.T) {
	assert.True(t, util.KeyMatch2("/api/v1/x/5", "/api/v1/x/*"))
	assert.False(t, util.KeyMatch2("/api/v1/x/5", "/api/v1/x*"), "why every collection is two entries")
	assert.Equal(t, []string{"GET /api/v1/x", "GET /api/v1/x/*", "POST /api/v1/x", "POST /api/v1/x/*"}, both("/api/v1/x", "GET", "POST"))
}

// setupPlatformDB opens a database with every table and the platform seeds, the way a first start does.
func setupPlatformDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(model.Models...))
	oldDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = oldDB })
	require.NoError(t, seedPlatformPermissions())
	return db
}

func TestThePagesOfUsersRolesAndLogsHaveTheirButtonsSeededUnderTheirMenusAndTheAdministratorHoldsThem(t *testing.T) {
	db := setupPlatformDB(t)
	want := map[string]string{
		"authorization:user:add": "authorization_user", "authorization:user:batchDelete": "authorization_user", "authorization:user:edit": "authorization_user",
		"authorization:user:resetOtp": "authorization_user", "authorization:user:delete": "authorization_user",
		"authorization:role:add": "authorization_role", "authorization:role:batchDelete": "authorization_role", "authorization:role:edit": "authorization_role",
		"authorization:role:delete": "authorization_role", "system:operation-log:clear": "system_operation-log",
	}
	var admin model.Role
	require.NoError(t, db.Preload("Permissions").Where("code = ?", "ADMIN").First(&admin).Error)
	held := map[string]bool{}
	for _, p := range admin.Permissions {
		held[p.Code] = true
	}
	for code, menuCode := range want {
		var button, menu model.Permission
		require.NoError(t, db.Where("code = ?", code).First(&button).Error, code)
		require.NoError(t, db.Where("code = ?", menuCode).First(&menu).Error, menuCode)
		assert.Equal(t, "BUTTON", button.Type, code)
		assert.Equal(t, menu.ID, button.ParentID, "%s sits under %s: the page reads its buttons from there", code, menuCode)
		assert.Contains(t, []string{"row", "header"}, button.Position, code)
		assert.NotEmpty(t, button.BeApis, "%s carries the route it needs", code)
		assert.True(t, held[code], "the administrator holds %s", code)
	}
	// a second run changes nothing
	require.NoError(t, seedPlatformPermissions())
	var count int64
	require.NoError(t, db.Model(&model.Permission{}).Where("code = ?", "authorization:role:edit").Count(&count).Error)
	assert.EqualValues(t, 1, count)
}
