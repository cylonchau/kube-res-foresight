package rdb

import (
	"context"
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"gorm.io/gorm"
	"strings"
	"time"
)

// ErrNodeNotPending is returned by ApproveWithToken when the node is no
// longer "pending" by the time the approval update runs — either it was
// already approved/rejected by a concurrent request, or it doesn't exist.
var ErrNodeNotPending = errors.New("bot-api node is not pending approval")

type TelegramBotAPINodeDAO struct {
	db *gorm.DB
}

// applyStaleness downgrades an online/approved node's displayed Status to
// "offline" once its last heartbeat is more than 3 minutes old. Purely a
// display computation (not persisted) — called from every read path
// (ListPaginated, GetByID) so the list and detail views never disagree.
func applyStaleness(n *model.TelegramBotAPINode, now time.Time) {
	if (n.Status == "online" || n.Status == "approved") &&
		(n.LastSeenAt == nil || now.Sub(*n.LastSeenAt) > 3*time.Minute) {
		n.Status = "offline"
	}
}

func NewTelegramBotAPINodeDAO(db *gorm.DB) *TelegramBotAPINodeDAO {
	return &TelegramBotAPINodeDAO{db: db}
}

func (dao *TelegramBotAPINodeDAO) GetByID(ctx context.Context, id int64) (*model.TelegramBotAPINode, error) {
	var node model.TelegramBotAPINode
	if err := dao.db.WithContext(ctx).First(&node, id).Error; err != nil {
		return nil, err
	}
	applyStaleness(&node, time.Now())
	return &node, nil
}

func (dao *TelegramBotAPINodeDAO) GetByName(ctx context.Context, name string) (*model.TelegramBotAPINode, error) {
	var node model.TelegramBotAPINode
	if err := dao.db.WithContext(ctx).Where("name = ?", name).First(&node).Error; err != nil {
		return nil, err
	}
	return &node, nil
}

func (dao *TelegramBotAPINodeDAO) ListPaginated(ctx context.Context, req *apis.TelegramBotAPINodeListReq) ([]*model.TelegramBotAPINode, int64, error) {
	query := dao.db.WithContext(ctx).Model(&model.TelegramBotAPINode{})
	if req.Keyword != nil && strings.TrimSpace(*req.Keyword) != "" {
		pattern := "%" + strings.TrimSpace(*req.Keyword) + "%"
		query = query.Where("name LIKE ? OR hostname LIKE ?", pattern, pattern)
	}
	if req.Status != nil && *req.Status != "" && *req.Status != "all" {
		query = query.Where("status = ?", *req.Status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, pageSize := req.Page, req.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 10
	}
	var nodes []*model.TelegramBotAPINode
	if err := query.Order("id DESC").Offset(int((page - 1) * pageSize)).Limit(int(pageSize)).Find(&nodes).Error; err != nil {
		return nil, 0, err
	}

	now := time.Now()
	for i := range nodes {
		applyStaleness(nodes[i], now)
	}
	return nodes, total, nil
}

// ApproveWithToken transitions a pending node to approved, storing the new
// bearer token's hash for future auth and stashing the encrypted plaintext
// for one-time delivery to the agent (cleared on its first heartbeat).
func (dao *TelegramBotAPINodeDAO) ApproveWithToken(ctx context.Context, id int64, tokenHash, issuedTokenEncrypted string) error {
	res := dao.db.WithContext(ctx).Model(&model.TelegramBotAPINode{}).
		Where("id = ? AND status = ?", id, "pending").
		Updates(map[string]interface{}{
			"status": "approved", "enabled": true,
			"token_hash": tokenHash, "issued_token_encrypted": issuedTokenEncrypted,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNodeNotPending
	}
	return nil
}

func (dao *TelegramBotAPINodeDAO) Reject(ctx context.Context, id int64) error {
	return dao.db.WithContext(ctx).Model(&model.TelegramBotAPINode{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{"status": "rejected", "enabled": false}).Error
}

func (dao *TelegramBotAPINodeDAO) SetEnabled(ctx context.Context, id int64, enabled bool) error {
	status := "online"
	if !enabled {
		status = "disabled"
	}
	return dao.db.WithContext(ctx).Model(&model.TelegramBotAPINode{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{"enabled": enabled, "status": status}).Error
}

func (dao *TelegramBotAPINodeDAO) Delete(ctx context.Context, id int64) error {
	return dao.db.WithContext(ctx).Unscoped().Delete(&model.TelegramBotAPINode{}, id).Error
}

// UpdateDesiredConfig applies an admin edit to a node's desired state.
// apiHashEncrypted is only applied when non-nil (caller decides whether the
// request carried a new plaintext hash to encrypt).
func (dao *TelegramBotAPINodeDAO) UpdateDesiredConfig(ctx context.Context, id int64, req *apis.TelegramBotAPINodeDesiredConfigReq, apiHashEncrypted *string) error {
	updates := map[string]interface{}{}
	if req.DesiredRunning != nil {
		updates["desired_running"] = *req.DesiredRunning
	}
	if req.Port != nil {
		updates["port"] = *req.Port
	}
	if req.LocalMode != nil {
		updates["local_mode"] = *req.LocalMode
	}
	if req.Image != nil && strings.TrimSpace(*req.Image) != "" {
		updates["image"] = strings.TrimSpace(*req.Image)
	}
	if req.APIID != nil {
		updates["api_id"] = strings.TrimSpace(*req.APIID)
	}
	if req.Remark != nil {
		updates["remark"] = *req.Remark
	}
	if apiHashEncrypted != nil {
		updates["api_hash_encrypted"] = *apiHashEncrypted
	}
	if len(updates) == 0 {
		return nil
	}
	return dao.db.WithContext(ctx).Model(&model.TelegramBotAPINode{}).Where("id = ?", id).Updates(updates).Error
}
