package api

import (
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/app/query"
	apputils "github.com/cylonchau/kubeforecast/pkg/app/utils"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	cryptoutils "github.com/cylonchau/kubeforecast/pkg/utils"
	"github.com/gin-gonic/gin"
	"strconv"
	"strings"
)

// BotAPINodeAdminRouter serves admin management of telegram-bot-api
// deployment nodes (list/approve/reject/enable/desired-config/delete). The
// check-in protocol the agent calls on itself lives in
// the Telegram Bot API agent router.
type BotAPINodeAdminRouter struct {
	DAO *rdb.TelegramBotAPINodeDAO
}

func botAPINodeDTO(n *model.TelegramBotAPINode) *apis.TelegramBotAPINodeDTO {
	if n == nil {
		return nil
	}
	return &apis.TelegramBotAPINodeDTO{
		ID: n.ID, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt,
		Name: n.Name, Status: n.Status, Enabled: n.Enabled,
		Hostname: n.Hostname, OperatingSystem: n.OperatingSystem, Architecture: n.Architecture,
		Version: n.Version, LastSeenAt: n.LastSeenAt, ClientIP: n.ClientIP,
		DesiredRunning: n.DesiredRunning, Port: n.Port, LocalMode: n.LocalMode,
		Image: n.Image, APIID: n.APIID, HasAPIHash: n.APIHashEncrypted != "",
		ObservedRunning: n.ObservedRunning, ObservedContainer: n.ObservedContainer,
		ObservedImage: n.ObservedImage, LastError: n.LastError, Remark: n.Remark,
	}
}

func botAPINodeDTOs(nodes []*model.TelegramBotAPINode) []*apis.TelegramBotAPINodeDTO {
	dtos := make([]*apis.TelegramBotAPINodeDTO, 0, len(nodes))
	for _, n := range nodes {
		dtos = append(dtos, botAPINodeDTO(n))
	}
	return dtos
}

// List returns a paginated list of bot-api nodes.
func (r *BotAPINodeAdminRouter) List(c *gin.Context) {
	req := &apis.TelegramBotAPINodeListReq{}
	if err := c.ShouldBindQuery(req); err != nil {
		query.BadRequest(c, errors.New(query.GetErrorMsg(req, err)))
		return
	}
	nodes, total, err := r.DAO.ListPaginated(c.Request.Context(), req)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": botAPINodeDTOs(nodes), "total": total})
}

// Get retrieves a bot-api node by ID.
func (r *BotAPINodeAdminRouter) Get(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		query.BadRequest(c, errors.New("invalid node id"))
		return
	}
	node, err := r.DAO.GetByID(c.Request.Context(), id)
	if err != nil {
		query.NotFound(c, query.ErrRecordNotFound)
		return
	}
	query.SuccessResponse(c, nil, botAPINodeDTO(node))
}

// Approve approves a pending node: it mints a new bearer token, stores its
// hash for future auth, and stashes the encrypted plaintext for one-time
// delivery to the agent on its next register poll (mirrors
// SSLDeploymentAdminRouter.ApproveConnector). The plaintext is never
// returned to the admin API caller.
func (r *BotAPINodeAdminRouter) Approve(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		query.BadRequest(c, errors.New("invalid node id"))
		return
	}
	node, err := r.DAO.GetByID(c.Request.Context(), id)
	if err != nil {
		query.NotFound(c, query.ErrRecordNotFound)
		return
	}
	if node.Status != "pending" {
		query.BadRequest(c, errors.New("node is not pending approval"))
		return
	}
	token := apputils.RandomHex(32)
	encrypted, err := cryptoutils.EncryptValue(token)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	if err := r.DAO.ApproveWithToken(c.Request.Context(), id, apputils.HashToken(token), encrypted); err != nil {
		if errors.Is(err, rdb.ErrNodeNotPending) {
			query.BadRequest(c, err)
			return
		}
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

// Reject rejects a pending node.
func (r *BotAPINodeAdminRouter) Reject(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		query.BadRequest(c, errors.New("invalid node id"))
		return
	}
	if err := r.DAO.Reject(c.Request.Context(), id); err != nil {
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

// SetEnabled enables or disables a node.
func (r *BotAPINodeAdminRouter) SetEnabled(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		query.BadRequest(c, errors.New("invalid node id"))
		return
	}
	var req apis.TelegramBotAPINodeSetEnabledReq
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		query.BadRequest(c, errors.New("enabled boolean is required"))
		return
	}
	if err := r.DAO.SetEnabled(c.Request.Context(), id, *req.Enabled); err != nil {
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

// UpdateDesiredConfig edits a node's desired container state.
func (r *BotAPINodeAdminRouter) UpdateDesiredConfig(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		query.BadRequest(c, errors.New("invalid node id"))
		return
	}
	var req apis.TelegramBotAPINodeDesiredConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	var apiHashEncrypted *string
	if req.APIHash != nil && strings.TrimSpace(*req.APIHash) != "" {
		encrypted, err := cryptoutils.EncryptValue(strings.TrimSpace(*req.APIHash))
		if err != nil {
			query.InternalError(c, err)
			return
		}
		apiHashEncrypted = &encrypted
	}
	if err := r.DAO.UpdateDesiredConfig(c.Request.Context(), id, &req, apiHashEncrypted); err != nil {
		query.HandleDBError(c, err)
		return
	}
	node, err := r.DAO.GetByID(c.Request.Context(), id)
	if err != nil {
		query.NotFound(c, query.ErrRecordNotFound)
		return
	}
	query.SuccessResponse(c, nil, botAPINodeDTO(node))
}

// Delete removes a node.
func (r *BotAPINodeAdminRouter) Delete(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		query.BadRequest(c, errors.New("invalid node id"))
		return
	}
	if err := r.DAO.Delete(c.Request.Context(), id); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}
