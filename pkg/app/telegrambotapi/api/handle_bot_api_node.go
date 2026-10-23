package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/app/agentauth"
	"github.com/cylonchau/kubeforecast/pkg/app/query"
	apputils "github.com/cylonchau/kubeforecast/pkg/app/utils"
	"github.com/cylonchau/kubeforecast/pkg/model"
	cryptoutils "github.com/cylonchau/kubeforecast/pkg/utils"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"strings"
	"time"
)

// BotAPINodeAgentRouter serves the check-in protocol a telegram-bot-api
// deployment agent calls on itself (register, heartbeat, config-snapshot
// pull). Admin-facing management lives in
// pkg/app/server/api.BotAPINodeAdminRouter. Calls here use bearer-token
// authentication because the config snapshot carries a Telegram api_hash.
type BotAPINodeAgentRouter struct{ DB *gorm.DB }

// Register handles both first contact (creates a pending or, with a valid
// bootstrap token, immediately-approved node) and subsequent polls while
// pending or approved.
func (r *BotAPINodeAgentRouter) Register(c *gin.Context) {
	var req apis.TelegramBotAPINodeRegisterReq
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		query.BadRequest(c, errors.New("name is required for bot-api node registration"))
		return
	}
	provided := agentauth.BearerToken(c.Request)
	bootstrapOK := agentauth.BootstrapTokenOK(provided)
	now := time.Now()

	if req.NodeID != 0 {
		var node model.TelegramBotAPINode
		err := r.DB.First(&node, req.NodeID).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			query.InternalError(c, err)
			return
		}
		if err == nil {
			finalToken := provided
			if finalToken == "" {
				finalToken = req.Token
			}
			if agentauth.TokensMatch(node.TokenHash, finalToken) {
				updates := map[string]interface{}{
					"name": req.Name, "hostname": req.Hostname, "operating_system": req.OperatingSystem,
					"architecture": req.Architecture, "version": req.Version,
					"client_ip": c.ClientIP(), "last_seen_at": &now,
				}
				if node.Enabled {
					updates["status"] = "online"
				} else {
					updates["status"] = "disabled"
				}
				if err := r.DB.Model(&node).Updates(updates).Error; err != nil {
					query.InternalError(c, err)
					return
				}
				query.SuccessResponse(c, nil, apis.TelegramBotAPINodeSession{NodeID: node.ID, Token: finalToken, Status: "online"})
				return
			}
			enrollmentToken := req.Token
			if enrollmentToken == "" {
				enrollmentToken = provided
			}
			if !agentauth.TokensMatch(node.EnrollmentTokenHash, enrollmentToken) {
				query.Unauthorized(c, errors.New("invalid node registration token"))
				return
			}
			if node.Status == "pending" {
				query.SuccessResponse(c, nil, apis.TelegramBotAPINodeSession{NodeID: node.ID, Status: "pending"})
				return
			}
			if node.Status == "approved" && node.IssuedTokenEncrypted != "" {
				token, err := cryptoutils.DecryptValue(node.IssuedTokenEncrypted)
				if err != nil {
					query.InternalError(c, errors.New("cannot deliver approved node token"))
					return
				}
				c.Header("Cache-Control", "no-store")
				query.SuccessResponse(c, nil, apis.TelegramBotAPINodeSession{NodeID: node.ID, Token: token, Status: "approved"})
				return
			}
			query.Forbidden(c, errors.New("node registration was rejected"))
			return
		}
	}

	node := model.TelegramBotAPINode{
		Name: req.Name, Hostname: req.Hostname, OperatingSystem: req.OperatingSystem,
		Architecture: req.Architecture, Version: req.Version, ClientIP: c.ClientIP(),
		Enabled: true, Port: 8081, Image: "aiogram/telegram-bot-api:latest",
	}
	if bootstrapOK {
		token := apputils.RandomHex(32)
		node.TokenHash, node.Status, node.LastSeenAt = apputils.HashToken(token), "online", &now
		if err := r.DB.Create(&node).Error; err != nil {
			query.InternalError(c, err)
			return
		}
		query.SuccessResponse(c, nil, apis.TelegramBotAPINodeSession{NodeID: node.ID, Token: token, Status: "online"})
		return
	}
	if len(strings.TrimSpace(req.Token)) < 32 || subtle.ConstantTimeCompare([]byte(provided), []byte(req.Token)) != 1 {
		query.BadRequest(c, errors.New("a secure agent-generated registration token is required"))
		return
	}
	node.EnrollmentTokenHash, node.Status = apputils.HashToken(req.Token), "pending"
	if err := r.DB.Create(&node).Error; err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, apis.TelegramBotAPINodeSession{NodeID: node.ID, Status: "pending"})
}

// Heartbeat updates observed state and, on the first heartbeat after
// approval, clears the one-time-issued token fields.
func (r *BotAPINodeAgentRouter) Heartbeat(c *gin.Context) {
	node, ok := r.authorize(c)
	if !ok {
		return
	}
	var req apis.TelegramBotAPINodeHeartbeatReq
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			query.BadRequest(c, err)
			return
		}
	}
	now := time.Now()
	status := "online"
	if !node.Enabled {
		status = "disabled"
	}
	updates := map[string]interface{}{
		"status": status, "last_seen_at": now, "client_ip": c.ClientIP(),
		"enrollment_token_hash": "", "issued_token_encrypted": "",
		"observed_running": req.ObservedRunning, "observed_container": req.ObservedContainer,
		"observed_image": req.ObservedImage, "last_error": req.LastError,
	}
	if req.Hostname != "" {
		updates["hostname"] = req.Hostname
	}
	if req.OperatingSystem != "" {
		updates["operating_system"] = req.OperatingSystem
	}
	if req.Architecture != "" {
		updates["architecture"] = req.Architecture
	}
	if req.Version != "" {
		updates["version"] = req.Version
	}
	if err := r.DB.Model(node).Updates(updates).Error; err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

// GetConfigSnapshot returns the node's desired state, including the
// decrypted API hash, to an authenticated & approved agent only.
func (r *BotAPINodeAgentRouter) GetConfigSnapshot(c *gin.Context) {
	node, ok := r.authorize(c)
	if !ok {
		return
	}
	if !node.Enabled {
		query.Forbidden(c, errors.New("node is disabled"))
		return
	}
	apiHash := ""
	if node.APIHashEncrypted != "" {
		decrypted, err := cryptoutils.DecryptValue(node.APIHashEncrypted)
		if err != nil {
			query.InternalError(c, errors.New("cannot decrypt api hash"))
			return
		}
		apiHash = decrypted
	}
	payload, _ := json.Marshal(struct {
		Running bool
		Port    int
		Local   bool
		Image   string
		APIID   string
		APIHash string
	}{node.DesiredRunning, node.Port, node.LocalMode, node.Image, node.APIID, apiHash})
	hash := sha256.Sum256(payload)

	c.Header("Cache-Control", "no-store")
	query.SuccessResponse(c, nil, apis.TelegramBotAPINodeConfigSnapshot{
		Version: hex.EncodeToString(hash[:]), DesiredRunning: node.DesiredRunning,
		Port: node.Port, LocalMode: node.LocalMode, Image: node.Image,
		APIID: node.APIID, APIHash: apiHash,
	})
}

func (r *BotAPINodeAgentRouter) authorize(c *gin.Context) (*model.TelegramBotAPINode, bool) {
	var node model.TelegramBotAPINode
	if err := r.DB.First(&node, "id = ?", c.Param("id")).Error; err != nil || !agentauth.TokensMatch(node.TokenHash, agentauth.BearerToken(c.Request)) {
		query.Unauthorized(c, errors.New("invalid node credentials"))
		return nil, false
	}
	return &node, true
}
