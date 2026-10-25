package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cylonchau/kube-resource-fcst/pkg/apis"
	"github.com/cylonchau/kube-resource-fcst/pkg/app/query"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"strconv"
	"strings"
)

type ColumnConfigRouter struct {
	MetaDAO      *rdb.ColumnMetaDAO
	ConfigDAO    *rdb.UserColumnConfigDAO
	SysConfigDAO *rdb.SysConfigDAO
}

const defaultVisibleColumnsConfigKey = "ui.table.default_visible_columns"

// defaultMaxVisibleColumns mirrors the seeded default for defaultVisibleColumnsConfigKey.
const defaultMaxVisibleColumns = 5

type columnCheckPayload struct {
	Key     string `json:"key"`
	Checked bool   `json:"checked"`
}

// enforceVisibleColumnLimit limits checked business columns. Structural selection, expand and
// operation columns are always available and do not consume the configured allowance.
func (h *ColumnConfigRouter) enforceVisibleColumnLimit(ctx context.Context, configJSON string) error {
	if h.SysConfigDAO == nil {
		return nil
	}

	max := defaultMaxVisibleColumns
	if cfg, err := h.SysConfigDAO.GetByKey(ctx, defaultVisibleColumnsConfigKey); err == nil && cfg != nil {
		if n, err := strconv.Atoi(strings.TrimSpace(cfg.ConfigValue)); err == nil && n > 0 {
			max = n
		}
	}

	var checks []columnCheckPayload
	if err := json.Unmarshal([]byte(configJSON), &checks); err != nil {
		// Malformed payloads are rejected downstream by the save itself; skip the limit check.
		return nil
	}

	checkedCount := 0
	for _, check := range checks {
		if check.Checked && !isStructuralColumnKey(check.Key) {
			checkedCount++
		}
	}

	if checkedCount > max {
		return fmt.Errorf("最多只能勾选 %d 列，当前勾选了 %d 个", max, checkedCount)
	}
	return nil
}

func isStructuralColumnKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "__selection__", "selection", "expand", "__expand__",
		"action", "actions", "operate", "operation", "operations", "__action__", "__operation__",
		"id":
		return true
	default:
		return false
	}
}

// GetMeta returns all available columns for a model via reflection
// @Summary Get model column metadata
// @Description Returns a list of available columns and their metadata (type, label, sortable) for a given page or model.
// @Tags Column Config
// @Accept json
// @Produce json
// @Param page query string false "Page name"
// @Param model query string false "Model name"
// @Success 200 {object} query.Response "Success"
// @Router /api/v1/columns/meta [get]
func (h *ColumnConfigRouter) GetMeta(c *gin.Context) {
	pageName := c.Query("page")
	modelName := c.Query("model")

	if pageName == "" && modelName == "" {
		query.BadRequest(c, nil)
		return
	}

	searchKey := pageName
	if searchKey == "" {
		searchKey = modelName
	}

	columns := h.MetaDAO.GetModelColumnsByPage(searchKey)
	query.SuccessResponse(c, nil, columns)
}

// GetConfig returns the user's saved column visibility/order config
// @Summary Get user column config
// @Description Retrieves the currently authenticated user's saved column configuration for a specific view.
// @Tags Column Config
// @Accept json
// @Produce json
// @Param view_name query string false "View name"
// @Param view query string false "View name (alias)"
// @Success 200 {object} query.Response "Success"
// @Router /api/v1/columns/config [get]
func (h *ColumnConfigRouter) GetConfig(c *gin.Context) {
	userIdVal, exists := c.Get("userId")
	if !exists {
		query.Unauthorized(c, nil)
		return
	}

	userId, ok := getContextUserID(userIdVal)
	if !ok {
		query.InternalError(c, nil)
		return
	}

	viewName := c.Query("view_name")
	if viewName == "" {
		viewName = c.Query("view")
	}
	if viewName == "" {
		query.BadRequest(c, nil)
		return
	}

	config, err := h.ConfigDAO.Get(c.Request.Context(), userId, viewName)
	if err != nil {
		// If not found, it's not strictly an error, just return empty
		if errors.Is(err, gorm.ErrRecordNotFound) || isMissingColumnConfigStorage(err) {
			query.SuccessResponse(c, nil, gin.H{"config": ""})
			return
		}
		query.SuccessResponse(c, nil, gin.H{"config": ""})
		return
	}

	query.SuccessResponse(c, nil, gin.H{"config": config.Config})
}

// UpdateConfig saves or updates user's column preferences
// @Summary Update user column config
// @Description Saves or updates the currently authenticated user's column visibility and order preferences for a specific view.
// @Tags Column Config
// @Accept json
// @Produce json
// @Param request body apis.ColumnConfigUpdateReq true "Configuration detail"
// @Success 200 {object} query.Response "Success"
// @Router /api/v1/columns/config [post]
func (h *ColumnConfigRouter) UpdateConfig(c *gin.Context) {
	userIdVal, exists := c.Get("userId")
	if !exists {
		query.Unauthorized(c, nil)
		return
	}

	userId, ok := getContextUserID(userIdVal)
	if !ok {
		query.InternalError(c, nil)
		return
	}

	var req apis.ColumnConfigUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}

	if err := h.enforceVisibleColumnLimit(c.Request.Context(), req.Config); err != nil {
		query.BadRequest(c, err)
		return
	}

	if err := h.ConfigDAO.Save(c.Request.Context(), userId, req.ViewName, req.Config); err != nil {
		query.InternalError(c, err)
		return
	}

	query.SuccessResponse(c, nil, "Saved")
}

func getContextUserID(value any) (uint, bool) {
	switch v := value.(type) {
	case uint:
		return v, true
	case uint64:
		return uint(v), true
	case uint32:
		return uint(v), true
	case int:
		if v < 0 {
			return 0, false
		}
		return uint(v), true
	case int64:
		if v < 0 {
			return 0, false
		}
		return uint(v), true
	case float64:
		if v < 0 {
			return 0, false
		}
		return uint(v), true
	case string:
		parsed, err := strconv.ParseUint(v, 10, 0)
		if err != nil {
			return 0, false
		}
		return uint(parsed), true
	default:
		return 0, false
	}
}

func isMissingColumnConfigStorage(err error) bool {
	if err == nil {
		return false
	}

	msg := strings.ToLower(fmt.Sprint(err))
	return strings.Contains(msg, "no such table") ||
		strings.Contains(msg, "doesn't exist") ||
		strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "unknown table")
}
