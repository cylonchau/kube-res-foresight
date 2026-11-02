package api

import (
	"context"
	"errors"
	"github.com/cylonchau/kube-res-foresight/pkg/apis"
	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/assistant"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/ldapauth"
	"github.com/cylonchau/kube-res-foresight/pkg/migration"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/notify/email"
	cryptoutils "github.com/cylonchau/kube-res-foresight/pkg/utils"
	"github.com/gin-gonic/gin"
	"strings"
	"time"
)

type ConfigRouter struct {
	DAO *rdb.SysConfigDAO
	// LDAP validates a change of an ldap.* parameter against the whole directory configuration.
	LDAP *LDAPRouter
}
type UpdateConfigReq = apis.ConfigUpdateReq

type sysConfigResponse struct {
	ID          uint      `json:"id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	ConfigKey   string    `json:"config_key"`
	ConfigValue string    `json:"config_value"`
	ConfigName  string    `json:"config_name"`
	Remark      string    `json:"remark"`
}

func sysConfigDTO(cfg *model.SysConfig) *sysConfigResponse {
	if cfg == nil {
		return nil
	}
	value := cfg.ConfigValue
	if model.IsSecretConfig(cfg.ConfigKey) && value != "" {
		value = SecretMask
	}

	return &sysConfigResponse{ID: cfg.ID, CreatedAt: cfg.CreatedAt, UpdatedAt: cfg.UpdatedAt, ConfigKey: cfg.ConfigKey, ConfigValue: value, ConfigName: cfg.ConfigName, Remark: cfg.Remark}
}

func sysConfigDTOs(configs []model.SysConfig) []*sysConfigResponse {
	result := make([]*sysConfigResponse, 0, len(configs))
	for i := range configs {
		if !IsInternalSysConfigKey(configs[i].ConfigKey) {
			result = append(result, sysConfigDTO(&configs[i]))
		}
	}
	return result
}

// SecretMask stands for a stored secret in the settings list; sending it back keeps the stored value.
const SecretMask = "********"

var internalSysConfigKeys = map[string]bool{"sys.auth.jwt_secret": true}

// IsInternalSysConfigKey hides the keys the generic settings API must never expose or accept: the signing secret.
// The LDAP parameters are ordinary system parameters; their secrets are masked and a change is validated as a whole.
func IsInternalSysConfigKey(key string) bool {
	return internalSysConfigKeys[strings.TrimSpace(key)]
}

func emptyAllowed(key string) bool {
	return strings.HasPrefix(key, ldapauth.KeyPrefix) || key == email.KeyHost || key == email.KeyUsername || key == email.KeyPassword || key == email.KeyFrom
}

func (h *ConfigRouter) List(c *gin.Context) {
	_ = migration.EnsureDefaultConfigs()
	configs, err := h.DAO.List(c.Request.Context())
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, sysConfigDTOs(configs))
}

func (h *ConfigRouter) Get(c *gin.Context) {
	key := c.Param("key")
	if IsInternalSysConfigKey(key) {
		query.NotFound(c, errors.New("config not found"))
		return
	}
	item, err := h.DAO.GetByKey(c.Request.Context(), key)
	if err != nil {
		query.NotFound(c, err)
		return
	}
	query.SuccessResponse(c, nil, sysConfigDTO(item))
}

func (h *ConfigRouter) Update(c *gin.Context) {
	key := c.Param("key")
	if IsInternalSysConfigKey(key) {
		query.NotFound(c, errors.New("config not found"))
		return
	}
	var body struct {
		ConfigValue *string `json:"config_value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.ConfigValue == nil {
		query.BadRequest(c, errors.New("config_value is required"))
		return
	}
	req := UpdateConfigReq{ConfigValue: *body.ConfigValue}
	// a value may be left empty where empty is a setting: the LDAP parameters (most of them are optional). Everything else must say something.
	if strings.TrimSpace(req.ConfigValue) == "" && !emptyAllowed(key) {
		query.BadRequest(c, errors.New("config_value is required"))
		return
	}
	if strings.HasPrefix(key, ldapauth.KeyPrefix) {
		if h.LDAP == nil {
			query.InternalError(c, errors.New("LDAP is not configured"))
			return
		}
		if err := h.LDAP.updateKey(c.Request.Context(), key, req.ConfigValue); err != nil {
			query.BadRequest(c, err)
			return
		}
		query.SuccessResponse(c, nil, "Config updated successfully")
		return
	}

	if key == email.KeyPassword {
		// the SMTP password: stored encrypted, shown as a mask, and the mask sent back keeps the stored one
		if req.ConfigValue == SecretMask {
			query.SuccessResponse(c, nil, "Config updated successfully")
			return
		}
		value := strings.TrimSpace(req.ConfigValue)
		if value != "" {
			enc, err := cryptoutils.EncryptValue(value)
			if err != nil {
				query.InternalError(c, err)
				return
			}
			value = enc
		}
		if err := h.DAO.UpdateValue(c.Request.Context(), key, value); err != nil {
			query.InternalError(c, err)
			return
		}
		query.SuccessResponse(c, nil, "Config updated successfully")
		return
	}
	if key == assistant.KeyEnabled {
		// the switch of the AI assistant; the language model it talks to is a connection in the menu AI assistant, not a parameter
		if err := assistant.UpdateEnabled(c.Request.Context(), h.DAO, req.ConfigValue); err != nil {
			query.BadRequest(c, err)
			return
		}
		query.SuccessResponse(c, nil, "Config updated successfully")
		return
	}

	if err := h.DAO.UpdateValue(context.Background(), key, req.ConfigValue); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "Config updated successfully")
}
