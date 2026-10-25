package api

import (
	"context"
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/app/query"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/migration"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/gin-gonic/gin"
	"strings"
	"time"
)

type ConfigRouter struct{ DAO *rdb.SysConfigDAO }
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

// IsInternalSysConfigKey hides the keys the generic settings API must never expose or accept: the signing secret
// and the whole LDAP configuration, which has its own validated, secret-aware panel.
func IsInternalSysConfigKey(key string) bool {
	key = strings.TrimSpace(key)
	return internalSysConfigKeys[key] || strings.HasPrefix(key, "ldap.")
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
	var req UpdateConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}

	if err := h.DAO.UpdateValue(context.Background(), key, req.ConfigValue); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "Config updated successfully")
}
