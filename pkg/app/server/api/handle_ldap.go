package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/app/query"
	"github.com/cylonchau/kubeforecast/pkg/ldapauth"
	"github.com/cylonchau/kubeforecast/pkg/model"
	cryptoutils "github.com/cylonchau/kubeforecast/pkg/utils"
	"github.com/gin-gonic/gin"
)

// LDAPRouter manages the directory configuration, group mappings and the account sync. Every route is
// administrator-only (no developer permission grants them).
type LDAPRouter struct {
	Service *ldapauth.Service
}

const ldapCheckTimeout = 30 * time.Second

func ldapContext(c *gin.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), d)
}

func ldapSettingDTO(row *model.LDAPSetting) apis.LDAPSettingDTO {
	if row == nil {
		return apis.LDAPSettingDTO{UserFilter: "(&(objectClass=person)(uid=%s))", DisplayNameAttr: "cn", EmailAttr: "mail", GroupNameAttr: "cn", TimeoutSeconds: 10}
	}
	return apis.LDAPSettingDTO{
		Enabled: row.Enabled, URL: row.URL, StartTLS: row.StartTLS, SkipVerify: row.SkipVerify, HasCACert: row.CACert != "",
		BindDN: row.BindDN, HasBindPassword: row.BindPassword != "", UserBaseDN: row.UserBaseDN, UserFilter: row.UserFilter,
		DisplayNameAttr: row.DisplayNameAttr, EmailAttr: row.EmailAttr, PhoneAttr: row.PhoneAttr, GroupBaseDN: row.GroupBaseDN,
		GroupFilter: row.GroupFilter, GroupNameAttr: row.GroupNameAttr, TimeoutSeconds: row.TimeoutSeconds, DefaultRoleCode: row.DefaultRoleCode,
		GroupRoleMappings: row.GroupRoleMappings,
	}
}

func (r *LDAPRouter) GetSettings(c *gin.Context) {
	row, _, err := r.Service.Load(c.Request.Context())
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, ldapSettingDTO(row))
}

func (r *LDAPRouter) UpdateSettings(c *gin.Context) {
	var req apis.LDAPSettingReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	ctx := c.Request.Context()
	current, _, err := r.Service.Load(ctx)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	row := &model.LDAPSetting{}
	if current != nil {
		*row = *current
	}
	row.Enabled, row.URL, row.StartTLS, row.SkipVerify = req.Enabled, strings.TrimSpace(req.URL), req.StartTLS, req.SkipVerify
	row.BindDN, row.UserBaseDN, row.UserFilter = strings.TrimSpace(req.BindDN), strings.TrimSpace(req.UserBaseDN), strings.TrimSpace(req.UserFilter)
	row.DisplayNameAttr, row.EmailAttr, row.PhoneAttr = strings.TrimSpace(req.DisplayNameAttr), strings.TrimSpace(req.EmailAttr), strings.TrimSpace(req.PhoneAttr)
	row.GroupBaseDN, row.GroupFilter, row.GroupNameAttr = strings.TrimSpace(req.GroupBaseDN), strings.TrimSpace(req.GroupFilter), strings.TrimSpace(req.GroupNameAttr)
	row.DefaultRoleCode = strings.TrimSpace(req.DefaultRoleCode)
	row.GroupRoleMappings = strings.TrimSpace(req.GroupRoleMappings)
	mappingList, err := ldapauth.ParseMappings(row.GroupRoleMappings)
	if err != nil {
		query.BadRequest(c, fmt.Errorf("group_role_mappings: %w", err))
		return
	}
	for _, m := range mappingList {
		var count int64
		if err := r.Service.DB.WithContext(ctx).Model(&model.Role{}).Where("code = ?", m.Role).Count(&count).Error; err != nil || count == 0 {
			query.BadRequest(c, fmt.Errorf("group_role_mappings: role %q does not exist", m.Role))
			return
		}
	}
	row.TimeoutSeconds = req.TimeoutSeconds
	if row.TimeoutSeconds == 0 {
		row.TimeoutSeconds = 10
	}
	if row.TimeoutSeconds < 1 || row.TimeoutSeconds > 120 {
		query.BadRequest(c, errors.New("timeout_seconds must be between 1 and 120"))
		return
	}
	if req.ClearCACert {
		row.CACert = ""
	}
	if strings.TrimSpace(req.CACert) != "" {
		row.CACert = strings.TrimSpace(req.CACert)
	}
	if strings.TrimSpace(req.BindPassword) != "" {
		enc, err := cryptoutils.EncryptValue(strings.TrimSpace(req.BindPassword))
		if err != nil {
			query.InternalError(c, err)
			return
		}
		row.BindPassword = enc
	}
	if row.BindDN == "" {
		row.BindPassword = ""
	}
	if row.DefaultRoleCode != "" {
		var count int64
		if err := r.Service.DB.WithContext(ctx).Model(&model.Role{}).Where("code = ? AND status = ?", row.DefaultRoleCode, true).Count(&count).Error; err != nil || count == 0 {
			query.BadRequest(c, fmt.Errorf("default role %q does not exist or is disabled", row.DefaultRoleCode))
			return
		}
	}
	rt, err := ldapauth.RuntimeSettings(row)
	if err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := rt.Validate(); err != nil {
		query.BadRequest(c, err)
		return
	}
	// Enabling is gated on a working connection, so a typo cannot silently turn directory login into a dead end.
	// Local accounts, the break-glass path, are unaffected either way.
	if row.Enabled {
		checkCtx, cancel := ldapContext(c, ldapCheckTimeout)
		defer cancel()
		if err := r.Service.Dir.Check(checkCtx, rt); err != nil {
			query.BadRequest(c, fmt.Errorf("the directory check failed, LDAP was not enabled: %w", err))
			return
		}
	}
	if err := r.Service.Save(ctx, row); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, ldapSettingDTO(row))
}

// Test checks the saved configuration and, with a username, resolves that user without a password.
func (r *LDAPRouter) Test(c *gin.Context) {
	var req apis.LDAPTestReq
	_ = c.ShouldBindJSON(&req)
	row, rt, err := r.Service.Load(c.Request.Context())
	if err != nil {
		query.InternalError(c, err)
		return
	}
	if row == nil {
		query.BadRequest(c, errors.New("save the LDAP settings first"))
		return
	}
	ctx, cancel := ldapContext(c, ldapCheckTimeout)
	defer cancel()
	result := apis.LDAPTestResult{OK: true}
	if err := rt.Validate(); err != nil {
		result = apis.LDAPTestResult{Error: err.Error()}
	} else if err := r.Service.Dir.Check(ctx, rt); err != nil {
		result = apis.LDAPTestResult{Error: err.Error()}
	} else if name := strings.TrimSpace(req.Username); name != "" {
		id, err := r.Service.Dir.Lookup(ctx, rt, name)
		if err != nil {
			result = apis.LDAPTestResult{Error: err.Error()}
		} else {
			roles, _ := r.Service.PreviewRoles(ctx, id)
			result.User = &apis.LDAPTestedUser{DN: id.DN, DisplayName: id.DisplayName, Email: id.Email, Groups: id.Groups, Disabled: id.Disabled, Roles: roles}
		}
	}
	query.SuccessResponse(c, nil, result)
}

// Sync re-checks every directory account now.
func (r *LDAPRouter) Sync(c *gin.Context) {
	ctx, cancel := ldapContext(c, 5*time.Minute)
	defer cancel()
	res, err := r.Service.SyncAll(ctx)
	if err != nil {
		query.BadRequest(c, err)
		return
	}
	query.SuccessResponse(c, nil, res)
}
