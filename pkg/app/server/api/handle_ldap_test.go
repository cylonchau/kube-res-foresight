package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/cylonchau/kube-resource-fcst/pkg/app/utils"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/ldapauth"
	"github.com/cylonchau/kube-resource-fcst/pkg/migration"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type stubDirectory struct {
	users    map[string]string // login name -> password
	groups   map[string][]string
	down     bool
	checkErr error
}

func (s *stubDirectory) Authenticate(_ context.Context, _ ldapauth.Settings, username, password string) (*ldapauth.Identity, error) {
	if s.down {
		return nil, errors.New("connection refused")
	}
	if want, ok := s.users[username]; !ok || password == "" || want != password {
		return nil, ldapauth.ErrInvalidCredentials
	}
	return &ldapauth.Identity{DN: "uid=" + username + ",dc=example,dc=org", Username: username, DisplayName: "LDAP " + username, Groups: s.groups[username]}, nil
}

func (s *stubDirectory) Lookup(_ context.Context, _ ldapauth.Settings, username string) (*ldapauth.Identity, error) {
	if s.down {
		return nil, errors.New("connection refused")
	}
	if _, ok := s.users[username]; !ok {
		return nil, ldapauth.ErrUserNotFound
	}
	return &ldapauth.Identity{DN: "uid=" + username + ",dc=example,dc=org", Username: username, Groups: s.groups[username]}, nil
}

func (s *stubDirectory) Check(context.Context, ldapauth.Settings) error {
	if s.down {
		return errors.New("connection refused")
	}
	return s.checkErr
}

var ldapDBSeq int32

type ldapAPI struct {
	router *gin.Engine
	db     *gorm.DB
	dir    *stubDirectory
}

func newLDAPAPI(t *testing.T) *ldapAPI {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:ldapapi_%d?mode=memory&cache=shared", atomic.AddInt32(&ldapDBSeq, 1))), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserContact{}, &model.Role{}, &model.Permission{}, &model.SysConfig{}, &model.SystemMarker{},
		&model.RefreshTokenSession{}, &model.LoginFailureRecord{}, &model.LoginCaptchaChallenge{}, &model.LoginAuditLog{}))
	previous := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previous })
	previousSecret := append([]byte(nil), utils.SecretKey...)
	utils.SetSecretKey([]byte("01234567890123456789012345678901"))
	t.Cleanup(func() { utils.SetSecretKey(previousSecret) })

	admin := model.Role{Name: "Admin", Code: "ADMIN", Status: true}
	viewer := model.Role{Name: "Viewer", Code: "VIEWER", Status: true}
	require.NoError(t, db.Create(&admin).Error)
	require.NoError(t, db.Create(&viewer).Error)
	hash, err := utils.HashPassword("local-pw")
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.User{Username: "admin", PasswordHash: hash, Status: true, Source: model.UserSourceLocal, Roles: []model.Role{admin}}).Error)

	dir := &stubDirectory{users: map[string]string{"lena": "lena-pw"}, groups: map[string][]string{"lena": {"ops"}}}
	refresh := rdb.NewRefreshTokenSessionDAO(db)
	svc := &ldapauth.Service{DB: db, Dir: dir, RevokeSessions: refresh.RevokeUser}
	auth := &AuthRouter{UserDAO: rdb.NewUserDAO(db), PermDAO: rdb.NewPermissionDAO(db), ConfigDAO: rdb.NewSysConfigDAO(db), RefreshDAO: refresh,
		SecurityDAO: rdb.NewLoginSecurityDAO(db), LoginAuditDAO: rdb.NewLoginAuditLogDAO(db), LDAP: svc}
	h := &LDAPRouter{Service: svc}

	r := gin.New()
	r.POST("/auth/login", auth.Login)
	r.GET("/ldap/settings", h.GetSettings)
	r.PUT("/ldap/settings", h.UpdateSettings)
	r.POST("/ldap/test", h.Test)
	r.POST("/ldap/sync", h.Sync)
	asLena := r.Group("/as-lena")
	asLena.Use(func(c *gin.Context) {
		var u model.User
		_ = db.Where("username = ?", "lena").First(&u).Error
		c.Set("userId", u.ID)
		c.Set("username", u.Username)
		c.Next()
	})
	asLena.PUT("/password", auth.UpdatePassword)
	return &ldapAPI{router: r, db: db, dir: dir}
}

func validSettings() map[string]any {
	return map[string]any{
		"enabled": true, "url": "ldaps://ldap.example.org", "bind_dn": "cn=svc,dc=example,dc=org", "bind_password": "svc-secret",
		"user_base_dn": "ou=people,dc=example,dc=org", "user_filter": "(&(objectClass=person)(uid=%s))", "display_name_attr": "cn", "email_attr": "mail",
		"group_base_dn": "ou=groups,dc=example,dc=org", "group_filter": "(&(objectClass=groupOfNames)(member=%s))", "group_name_attr": "cn",
		"group_role_mappings": "ops=VIEWER",
	}
}

func TestSettingsAreValidatedGatedOnACheckAndNeverReturnSecrets(t *testing.T) {
	a := newLDAPAPI(t)
	rec := performJSON(t, a.router, http.MethodGet, "/ldap/settings", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, false, responseData(t, rec.Body.Bytes())["enabled"])

	bad := validSettings()
	bad["url"] = "ldap://ldap.example.org"
	assert.Equal(t, http.StatusBadRequest, performJSON(t, a.router, http.MethodPut, "/ldap/settings", "", bad).Code, "plain ldap without start_tls")
	bad = validSettings()
	bad["user_filter"] = "(uid=bob)"
	assert.Equal(t, http.StatusBadRequest, performJSON(t, a.router, http.MethodPut, "/ldap/settings", "", bad).Code)
	bad = validSettings()
	bad["default_role_code"] = "NOPE"
	assert.Equal(t, http.StatusBadRequest, performJSON(t, a.router, http.MethodPut, "/ldap/settings", "", bad).Code)

	a.dir.checkErr = errors.New("bind service account: invalid credentials")
	rec = performJSON(t, a.router, http.MethodPut, "/ldap/settings", "", validSettings())
	require.Equal(t, http.StatusBadRequest, rec.Code, "enabling needs a working directory")
	assert.Contains(t, rec.Body.String(), "LDAP was not enabled")
	var count int64
	require.NoError(t, a.db.Model(&model.SysConfig{}).Where("config_key LIKE ?", "ldap.%").Count(&count).Error)
	assert.Zero(t, count, "nothing was saved")

	a.dir.checkErr = nil
	rec = performJSON(t, a.router, http.MethodPut, "/ldap/settings", "", validSettings())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "svc-secret")
	data := responseData(t, rec.Body.Bytes())
	assert.Equal(t, true, data["enabled"])
	assert.Equal(t, true, data["has_bind_password"])

	var stored model.SysConfig
	require.NoError(t, a.db.Where("config_key = ?", "ldap.bind_password").First(&stored).Error)
	assert.NotEmpty(t, stored.ConfigValue)
	assert.NotContains(t, stored.ConfigValue, "svc-secret", "the bind password is stored encrypted")

	// a blank password on update keeps the stored one
	update := validSettings()
	delete(update, "bind_password")
	update["start_tls"] = false
	rec = performJSON(t, a.router, http.MethodPut, "/ldap/settings", "", update)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, responseData(t, rec.Body.Bytes())["has_bind_password"])
}

func TestDirectoryUsersLogInAndGetTheirMappedRoles(t *testing.T) {
	a := newLDAPAPI(t)
	require.Equal(t, http.StatusOK, performJSON(t, a.router, http.MethodPut, "/ldap/settings", "", validSettings()).Code)

	rec := performJSON(t, a.router, http.MethodPost, "/auth/login", "", map[string]any{"username": "lena", "password": "lena-pw"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotEmpty(t, responseData(t, rec.Body.Bytes())["refreshToken"])

	var lena model.User
	require.NoError(t, a.db.Preload("Roles").Where("username = ?", "lena").First(&lena).Error)
	assert.Equal(t, model.UserSourceLDAP, lena.Source)
	require.Len(t, lena.Roles, 1)
	assert.Equal(t, "VIEWER", lena.Roles[0].Code)

	assert.Equal(t, http.StatusUnauthorized, performJSON(t, a.router, http.MethodPost, "/auth/login", "", map[string]any{"username": "lena", "password": "wrong", "force": true}).Code)
	assert.Equal(t, http.StatusUnauthorized, performJSON(t, a.router, http.MethodPost, "/auth/login", "", map[string]any{"username": "stranger", "password": "x"}).Code)
}

func TestTheLocalAdminStillLogsInWhenLDAPIsEnabledAndDown(t *testing.T) {
	a := newLDAPAPI(t)
	require.Equal(t, http.StatusOK, performJSON(t, a.router, http.MethodPut, "/ldap/settings", "", validSettings()).Code)
	a.dir.down = true

	rec := performJSON(t, a.router, http.MethodPost, "/auth/login", "", map[string]any{"username": "admin", "password": "local-pw"})
	assert.Equal(t, http.StatusOK, rec.Code, "the break-glass account does not depend on the directory")
	assert.Equal(t, http.StatusUnauthorized, performJSON(t, a.router, http.MethodPost, "/auth/login", "", map[string]any{"username": "lena", "password": "lena-pw"}).Code, "directory users cannot log in while it is down")

	// the local admin cannot be taken over by a directory entry of the same name
	a.dir.down = false
	a.dir.users["admin"] = "ldap-admin-pw"
	a.dir.groups["admin"] = []string{"ops"}
	assert.Equal(t, http.StatusUnauthorized, performJSON(t, a.router, http.MethodPost, "/auth/login", "", map[string]any{"username": "admin", "password": "ldap-admin-pw", "force": true}).Code)
	assert.Equal(t, http.StatusOK, performJSON(t, a.router, http.MethodPost, "/auth/login", "", map[string]any{"username": "admin", "password": "local-pw", "force": true}).Code)
}

func TestWithLDAPDisabledNothingChangesForLocalLogin(t *testing.T) {
	a := newLDAPAPI(t)
	assert.Equal(t, http.StatusOK, performJSON(t, a.router, http.MethodPost, "/auth/login", "", map[string]any{"username": "admin", "password": "local-pw"}).Code)
	assert.Equal(t, http.StatusUnauthorized, performJSON(t, a.router, http.MethodPost, "/auth/login", "", map[string]any{"username": "lena", "password": "lena-pw"}).Code)
}

func TestDirectoryUsersCannotSetALocalPassword(t *testing.T) {
	a := newLDAPAPI(t)
	require.Equal(t, http.StatusOK, performJSON(t, a.router, http.MethodPut, "/ldap/settings", "", validSettings()).Code)
	require.Equal(t, http.StatusOK, performJSON(t, a.router, http.MethodPost, "/auth/login", "", map[string]any{"username": "lena", "password": "lena-pw"}).Code)
	rec := performJSON(t, a.router, http.MethodPut, "/as-lena/password", "", map[string]any{"old_password": "x", "password": "new-pass-1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "directory")
}

func TestGroupRoleMappingsAreAParameterValidatedOnSave(t *testing.T) {
	a := newLDAPAPI(t)
	put := func(mappings string) int {
		body := validSettings()
		body["group_role_mappings"] = mappings
		return performJSON(t, a.router, http.MethodPut, "/ldap/settings", "", body).Code
	}
	assert.Equal(t, http.StatusBadRequest, put("sre"), "a line needs group=role")
	assert.Equal(t, http.StatusBadRequest, put("sre=NOPE"), "the role must exist")
	assert.Equal(t, http.StatusBadRequest, put("sre=ADMIN\nSRE=ADMIN"), "a duplicate line is refused")
	assert.Equal(t, http.StatusBadRequest, put(" =ADMIN"))
	require.Equal(t, http.StatusOK, put("# who may do what\nsre=ADMIN\n\ndev=VIEWER\nsre=VIEWER"))

	rec := performJSON(t, a.router, http.MethodGet, "/ldap/settings", "", nil)
	assert.Contains(t, responseData(t, rec.Body.Bytes())["group_role_mappings"], "sre=ADMIN")
}

// LDAP is an ordinary group of system parameters: listed, edited one by one, secrets masked, and the whole
// configuration is validated when a change would leave directory login enabled.
func TestLDAPParametersAreEditedLikeAnyOtherSystemParameter(t *testing.T) {
	a := newLDAPAPI(t)
	require.NoError(t, migration.EnsureDefaultConfigs())
	cfg := &ConfigRouter{DAO: rdb.NewSysConfigDAO(a.db), LDAP: &LDAPRouter{Service: &ldapauth.Service{DB: a.db, Dir: a.dir}}}
	a.router.GET("/configs", cfg.List)
	a.router.PUT("/configs/:key", cfg.Update)
	set := func(key, value string) *httptest.ResponseRecorder {
		return performJSON(t, a.router, http.MethodPut, "/configs/"+key, "", map[string]any{"config_value": value})
	}
	value := func(key string) string {
		var row model.SysConfig
		require.NoError(t, a.db.Where("config_key = ?", key).First(&row).Error)
		return row.ConfigValue
	}

	list := performJSON(t, a.router, http.MethodGet, "/configs", "", nil).Body.String()
	assert.Contains(t, list, "ldap.url", "the LDAP parameters are in the list")
	assert.NotContains(t, list, "sys.auth.jwt_secret")

	assert.Equal(t, http.StatusBadRequest, set("ldap.enabled", "true").Code, "incomplete settings cannot be enabled")
	assert.Equal(t, "false", value("ldap.enabled"))
	assert.Equal(t, http.StatusBadRequest, set("ldap.start_tls", "maybe").Code)
	assert.Equal(t, http.StatusBadRequest, set("ldap.timeout_seconds", "500").Code)

	for key, v := range map[string]string{"ldap.url": "ldaps://ldap.example.org", "ldap.user_base_dn": "ou=people,dc=example,dc=org",
		"ldap.bind_dn": "cn=svc,dc=example,dc=org", "ldap.bind_password": "svc-secret"} {
		require.Equal(t, http.StatusOK, set(key, v).Code, key)
	}
	assert.NotEqual(t, "svc-secret", value("ldap.bind_password"), "the password is stored encrypted")
	assert.NotContains(t, performJSON(t, a.router, http.MethodGet, "/configs", "", nil).Body.String(), "svc-secret")
	assert.Contains(t, performJSON(t, a.router, http.MethodGet, "/configs", "", nil).Body.String(), SecretMask)

	stored := value("ldap.bind_password")
	require.Equal(t, http.StatusOK, set("ldap.bind_password", SecretMask).Code)
	assert.Equal(t, stored, value("ldap.bind_password"), "sending the mask back keeps the secret")

	a.dir.down = true
	assert.Equal(t, http.StatusBadRequest, set("ldap.enabled", "true").Code, "enabling checks the directory")
	a.dir.down = false
	assert.Equal(t, http.StatusOK, set("ldap.enabled", "true").Code)
	assert.Equal(t, "true", value("ldap.enabled"))
}

func TestTestEndpointReportsTheConnectionAndTheRolesAUserWouldGet(t *testing.T) {
	a := newLDAPAPI(t)
	assert.Equal(t, http.StatusBadRequest, performJSON(t, a.router, http.MethodPost, "/ldap/test", "", map[string]any{}).Code, "nothing saved yet")
	require.Equal(t, http.StatusOK, performJSON(t, a.router, http.MethodPut, "/ldap/settings", "", validSettings()).Code)

	rec := performJSON(t, a.router, http.MethodPost, "/ldap/test", "", map[string]any{"username": "lena"})
	require.Equal(t, http.StatusOK, rec.Code)
	data := responseData(t, rec.Body.Bytes())
	assert.Equal(t, true, data["ok"])
	user := data["user"].(map[string]any)
	assert.Equal(t, []any{"ops"}, user["groups"])
	assert.Equal(t, []any{"VIEWER"}, user["roles"])

	data = responseData(t, performJSON(t, a.router, http.MethodPost, "/ldap/test", "", map[string]any{"username": "ghost"}).Body.Bytes())
	assert.Equal(t, false, data["ok"])
	assert.Contains(t, data["error"], "not found")

	a.dir.down = true
	data = responseData(t, performJSON(t, a.router, http.MethodPost, "/ldap/test", "", nil).Body.Bytes())
	assert.Equal(t, false, data["ok"])
}

func TestSyncEndpointDisablesAccountsTheDirectoryDropped(t *testing.T) {
	a := newLDAPAPI(t)
	require.Equal(t, http.StatusOK, performJSON(t, a.router, http.MethodPut, "/ldap/settings", "", validSettings()).Code)
	require.Equal(t, http.StatusOK, performJSON(t, a.router, http.MethodPost, "/auth/login", "", map[string]any{"username": "lena", "password": "lena-pw"}).Code)

	delete(a.dir.users, "lena")
	rec := performJSON(t, a.router, http.MethodPost, "/ldap/sync", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := responseData(t, rec.Body.Bytes())
	assert.EqualValues(t, 1, data["checked"])
	assert.EqualValues(t, 1, data["disabled"])

	assert.Equal(t, http.StatusUnauthorized, performJSON(t, a.router, http.MethodPost, "/auth/login", "", map[string]any{"username": "lena", "password": "lena-pw", "force": true}).Code)

	a.dir.down = true
	assert.Equal(t, http.StatusBadRequest, performJSON(t, a.router, http.MethodPost, "/ldap/sync", "", nil).Code, "an unreachable directory changes nobody")
}
