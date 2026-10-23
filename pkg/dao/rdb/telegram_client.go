package rdb

import (
	"context"
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/model"
	cryptoutils "github.com/cylonchau/kubeforecast/pkg/utils"
	"gorm.io/gorm"
	"strings"
	"time"
)

type TelegramClientDAO struct{ db *gorm.DB }

func NewTelegramClientDAO(db *gorm.DB) *TelegramClientDAO { return &TelegramClientDAO{db: db} }

// telegramClientFreshness is how recently the Telegram Agent must have
// reported an account for the API to call it connected.
const telegramClientFreshness = 90 * time.Second

func populateTelegramClientAccount(row *model.TelegramClientAccount) {
	if row == nil {
		return
	}
	row.HasAPIHash = row.APIHashEncrypted != ""
	row.HasSession = row.SessionEncrypted != ""
	row.Connected = row.ConnectionStatus == model.TelegramClientConnected && row.SeenAt != nil && time.Since(*row.SeenAt) <= telegramClientFreshness
}

func (d *TelegramClientDAO) ListAccounts(ctx context.Context, req *apis.TelegramClientAccountListReq) ([]model.TelegramClientAccount, int64, error) {
	var rows []model.TelegramClientAccount
	db := d.db.WithContext(ctx).Model(&model.TelegramClientAccount{})
	if q := strings.TrimSpace(req.Keyword); q != "" {
		like := "%" + q + "%"
		db = db.Where("name LIKE ? OR phone_number LIKE ? OR telegram_username LIKE ?", like, like, like)
	}
	if req.Enabled != nil {
		db = db.Where("enabled = ?", *req.Enabled)
	}
	if req.Authorized != nil {
		db = db.Where("authorized = ?", *req.Authorized)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := db.Order("id DESC").Offset(int(req.Offset())).Limit(int(req.PageSize)).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	for i := range rows {
		populateTelegramClientAccount(&rows[i])
	}
	return rows, total, nil
}

func (d *TelegramClientDAO) ListRunnableAccounts(ctx context.Context) ([]model.TelegramClientAccount, error) {
	var rows []model.TelegramClientAccount
	err := d.db.WithContext(ctx).Where("enabled = ? AND authorized = ? AND session_encrypted <> ''", true, true).Find(&rows).Error
	return rows, err
}

// ListRunnableAccountsFor returns the runnable accounts one TG Agent may run:
// the ones bound to it and the ones no agent has claimed yet.
func (d *TelegramClientDAO) ListRunnableAccountsFor(ctx context.Context, agentID string) ([]model.TelegramClientAccount, error) {
	var rows []model.TelegramClientAccount
	err := d.db.WithContext(ctx).
		Where("enabled = ? AND authorized = ? AND session_encrypted <> '' AND (agent_id = '' OR agent_id IS NULL OR agent_id = ?)", true, true, agentID).
		Find(&rows).Error
	return rows, err
}

// FirstConnectedAccount returns the account a TG Agent keeps connected: the one
// named by id, or the oldest when id is 0; nil when there is none.
func (d *TelegramClientDAO) FirstConnectedAccount(ctx context.Context, id int64) (*model.TelegramClientAccount, error) {
	var row model.TelegramClientAccount
	db := d.db.WithContext(ctx).Where("enabled = ? AND authorized = ? AND connection_status = ?", true, true, "connected")
	if id != 0 {
		db = db.Where("id = ?", id)
	} else {
		// automatic choice: an account Telegram has limited is passed over; a chosen one is returned so the caller can say why it is not used
		db = db.Where("send_blocked_until IS NULL OR send_blocked_until <= ?", time.Now().UTC())
	}
	err := db.Order("id ASC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &row, err
}

// BlockSending records that Telegram limited an account's sending until a time.
func (d *TelegramClientDAO) BlockSending(ctx context.Context, id int64, until time.Time, reason string) error {
	return d.db.WithContext(ctx).Model(&model.TelegramClientAccount{}).Where("id = ?", id).
		Updates(map[string]interface{}{"send_blocked_until": until.UTC(), "send_blocked_reason": reason}).Error
}

// BindAgent records which TG Agent runs an account.
func (d *TelegramClientDAO) BindAgent(ctx context.Context, id int64, agentID string) error {
	return d.db.WithContext(ctx).Model(&model.TelegramClientAccount{}).Where("id = ?", id).Update("agent_id", agentID).Error
}

func (d *TelegramClientDAO) GetAccount(ctx context.Context, id int64) (*model.TelegramClientAccount, error) {
	var row model.TelegramClientAccount
	if err := d.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, err
	}
	populateTelegramClientAccount(&row)
	return &row, nil
}

func (d *TelegramClientDAO) CreateAccount(ctx context.Context, req *apis.TelegramClientAccountCreateReq) (*model.TelegramClientAccount, error) {
	hash, err := cryptoutils.EncryptValue(strings.TrimSpace(req.APIHash))
	if err != nil {
		return nil, err
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	row := &model.TelegramClientAccount{Name: strings.TrimSpace(req.Name), PhoneNumber: strings.TrimSpace(req.PhoneNumber), APIID: req.APIID, APIHashEncrypted: hash, Enabled: enabled}
	if row.Name == "" || row.PhoneNumber == "" || row.APIID <= 0 {
		return nil, errors.New("name, phone_number and a positive api_id are required")
	}
	if err := d.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, err
	}
	populateTelegramClientAccount(row)
	return row, nil
}

func (d *TelegramClientDAO) UpdateAccount(ctx context.Context, id int64, req *apis.TelegramClientAccountUpdateReq) (*model.TelegramClientAccount, error) {
	updates := map[string]interface{}{}
	if req.Name != nil {
		v := strings.TrimSpace(*req.Name)
		if v == "" {
			return nil, errors.New("name cannot be empty")
		}
		updates["name"] = v
	}
	if req.PhoneNumber != nil {
		v := strings.TrimSpace(*req.PhoneNumber)
		if v == "" {
			return nil, errors.New("phone_number cannot be empty")
		}
		updates["phone_number"] = v
		updates["authorized"] = false
		updates["session_encrypted"] = ""
		updates["phone_code_hash"] = ""
		updates["code_expires_at"] = nil
	}
	if req.APIID != nil {
		if *req.APIID <= 0 {
			return nil, errors.New("api_id must be positive")
		}
		updates["api_id"] = *req.APIID
		updates["authorized"] = false
		updates["session_encrypted"] = ""
		updates["phone_code_hash"] = ""
		updates["code_expires_at"] = nil
	}
	if req.APIHash != nil && strings.TrimSpace(*req.APIHash) != "" {
		encrypted, err := cryptoutils.EncryptValue(strings.TrimSpace(*req.APIHash))
		if err != nil {
			return nil, err
		}
		updates["api_hash_encrypted"] = encrypted
		updates["authorized"] = false
		updates["session_encrypted"] = ""
		updates["phone_code_hash"] = ""
		updates["code_expires_at"] = nil
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if len(updates) > 0 {
		if err := d.db.WithContext(ctx).Model(&model.TelegramClientAccount{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return nil, err
		}
	}
	return d.GetAccount(ctx, id)
}

func (d *TelegramClientDAO) DeleteAccount(ctx context.Context, id int64) error {
	return d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("account_id = ?", id).Delete(&model.TelegramClientMessage{}).Error; err != nil {
			return err
		}
		if err := tx.Where("account_id = ?", id).Delete(&model.TelegramClientSubscription{}).Error; err != nil {
			return err
		}
		return tx.Unscoped().Delete(&model.TelegramClientAccount{}, id).Error
	})
}

func (d *TelegramClientDAO) DecryptAPIHash(account *model.TelegramClientAccount) (string, error) {
	return cryptoutils.DecryptValue(account.APIHashEncrypted)
}
func (d *TelegramClientDAO) LoadSession(ctx context.Context, accountID int64) ([]byte, error) {
	var encrypted string
	if err := d.db.WithContext(ctx).Model(&model.TelegramClientAccount{}).Where("id = ?", accountID).Pluck("session_encrypted", &encrypted).Error; err != nil {
		return nil, err
	}
	if encrypted == "" {
		return nil, nil
	}
	plain, err := cryptoutils.DecryptValue(encrypted)
	return []byte(plain), err
}
func (d *TelegramClientDAO) StoreSession(ctx context.Context, accountID int64, data []byte) error {
	encrypted := ""
	var err error
	if len(data) > 0 {
		encrypted, err = cryptoutils.EncryptValue(string(data))
		if err != nil {
			return err
		}
	}
	return d.db.WithContext(ctx).Model(&model.TelegramClientAccount{}).Where("id = ?", accountID).Update("session_encrypted", encrypted).Error
}
func (d *TelegramClientDAO) SaveCodeHash(ctx context.Context, id int64, hash string, expires time.Time) error {
	return d.db.WithContext(ctx).Model(&model.TelegramClientAccount{}).Where("id = ?", id).Updates(map[string]interface{}{"phone_code_hash": hash, "code_expires_at": expires, "last_error": ""}).Error
}
func (d *TelegramClientDAO) MarkAuthorized(ctx context.Context, id int64, userID int64, username, firstName, lastName string) error {
	now := time.Now()
	return d.db.WithContext(ctx).Model(&model.TelegramClientAccount{}).Where("id = ?", id).Updates(map[string]interface{}{"authorized": true, "telegram_user_id": userID, "telegram_username": username, "telegram_first_name": firstName, "telegram_last_name": lastName, "phone_code_hash": "", "code_expires_at": nil, "last_connected_at": &now, "last_error": ""}).Error
}
func (d *TelegramClientDAO) MarkUnauthorized(ctx context.Context, id int64) error {
	return d.db.WithContext(ctx).Model(&model.TelegramClientAccount{}).Where("id = ?", id).Updates(map[string]interface{}{"authorized": false, "session_encrypted": "", "phone_code_hash": "", "code_expires_at": nil, "connection_status": model.TelegramClientDisconnected}).Error
}

// SetConnectionResult records the outcome of (re)connecting an account.
func (d *TelegramClientDAO) SetConnectionResult(ctx context.Context, id int64, connected bool, message string) error {
	now := time.Now()
	updates := map[string]interface{}{"last_error": message, "seen_at": &now, "connection_status": model.TelegramClientDisconnected}
	if connected {
		updates["last_connected_at"] = &now
		updates["connection_status"] = model.TelegramClientConnected
	} else if message != "" {
		updates["connection_status"] = model.TelegramClientError
	}
	return d.db.WithContext(ctx).Model(&model.TelegramClientAccount{}).Where("id = ?", id).Updates(updates).Error
}

// ResetConnectionStatesFor marks the accounts one agent may run disconnected;
// it never touches accounts bound to another agent.
func (d *TelegramClientDAO) ResetConnectionStatesFor(ctx context.Context, agentID string) error {
	return d.db.WithContext(ctx).Model(&model.TelegramClientAccount{}).
		Where("connection_status <> ? AND (agent_id = '' OR agent_id IS NULL OR agent_id = ?)", model.TelegramClientDisconnected, agentID).
		Update("connection_status", model.TelegramClientDisconnected).Error
}

// TouchAccounts refreshes the heartbeat of the accounts the agent holds.
func (d *TelegramClientDAO) TouchAccounts(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now()
	return d.db.WithContext(ctx).Model(&model.TelegramClientAccount{}).Where("id IN ? AND connection_status = ?", ids, model.TelegramClientConnected).Update("seen_at", &now).Error
}

// ResetConnectionStates marks every account disconnected; the agent calls it
// on start, before it reconnects the ones it runs.
func (d *TelegramClientDAO) ResetConnectionStates(ctx context.Context) error {
	return d.db.WithContext(ctx).Model(&model.TelegramClientAccount{}).Where("connection_status <> ?", model.TelegramClientDisconnected).
		Update("connection_status", model.TelegramClientDisconnected).Error
}

func (d *TelegramClientDAO) ListSubscriptions(ctx context.Context, req *apis.TelegramClientSubscriptionListReq) ([]model.TelegramClientSubscription, int64, error) {
	var rows []model.TelegramClientSubscription
	db := d.db.WithContext(ctx).Model(&model.TelegramClientSubscription{})
	if req.AccountID > 0 {
		db = db.Where("account_id = ?", req.AccountID)
	}
	if req.Enabled != nil {
		db = db.Where("enabled = ?", *req.Enabled)
	}
	if req.State != "" {
		db = db.Where("state = ?", req.State)
	}
	if q := strings.TrimSpace(req.Keyword); q != "" {
		like := "%" + q + "%"
		db = db.Where("title LIKE ? OR username LIKE ? OR keywords LIKE ?", like, like, like)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := db.Preload("Account").Order("id DESC").Offset(int(req.Offset())).Limit(int(req.PageSize)).Find(&rows).Error
	return rows, total, err
}
func (d *TelegramClientDAO) GetEnabledSubscription(ctx context.Context, accountID, peerID int64) (*model.TelegramClientSubscription, error) {
	var row model.TelegramClientSubscription
	err := d.db.WithContext(ctx).Where("account_id = ? AND peer_id = ? AND enabled = ?", accountID, peerID, true).First(&row).Error
	return &row, err
}

// AdvanceSubscriptionPosition moves a subscription's read position forward; it never moves back.
func (d *TelegramClientDAO) AdvanceSubscriptionPosition(ctx context.Context, id int64, messageID int) error {
	return d.db.WithContext(ctx).Model(&model.TelegramClientSubscription{}).
		Where("id = ? AND last_message_id < ?", id, messageID).UpdateColumn("last_message_id", messageID).Error
}

// ObserveSubscriptionLatest records the newest message id seen in a chat; it never moves back.
func (d *TelegramClientDAO) ObserveSubscriptionLatest(ctx context.Context, id int64, latest int) error {
	now := time.Now()
	return d.db.WithContext(ctx).Model(&model.TelegramClientSubscription{}).Where("id = ? AND latest_message_id < ?", id, latest).
		UpdateColumns(map[string]any{"latest_message_id": latest, "latest_seen_at": &now}).Error
}

// ListJoinedSubscriptions returns the enabled subscriptions an account is a member of.
func (d *TelegramClientDAO) ListJoinedSubscriptions(ctx context.Context, accountID int64) ([]model.TelegramClientSubscription, error) {
	var rows []model.TelegramClientSubscription
	err := d.db.WithContext(ctx).Where("account_id = ? AND enabled = ? AND state = ?", accountID, true, model.TelegramSubJoined).Order("id ASC").Find(&rows).Error
	return rows, err
}

// MaxCachedMessageID is the newest Telegram message id of a chat still in the cache; 0 when none.
func (d *TelegramClientDAO) MaxCachedMessageID(ctx context.Context, accountID, peerID int64) (int, error) {
	var max *int
	err := d.db.WithContext(ctx).Model(&model.TelegramClientMessage{}).
		Where("account_id = ? AND peer_id = ?", accountID, peerID).Select("MAX(telegram_message_id)").Scan(&max).Error
	if max == nil {
		return 0, err
	}
	return *max, err
}

func (d *TelegramClientDAO) CreateSubscription(ctx context.Context, row *model.TelegramClientSubscription) error {
	if row.State == "" {
		row.State = model.TelegramSubPending
	}
	return d.db.WithContext(ctx).Create(row).Error
}

func (d *TelegramClientDAO) GetSubscription(ctx context.Context, id int64) (*model.TelegramClientSubscription, error) {
	var row model.TelegramClientSubscription
	if err := d.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// SetSubscriptionState records the membership the agent observed. A left
// subscription is also disabled so it stops receiving.
func (d *TelegramClientDAO) SetSubscriptionState(ctx context.Context, id int64, state, message string) error {
	updates := map[string]interface{}{"state": state, "last_error": message}
	if state == model.TelegramSubLeft {
		updates["enabled"] = false
	}
	return d.db.WithContext(ctx).Model(&model.TelegramClientSubscription{}).Where("id = ?", id).Updates(updates).Error
}

// FindSubscription returns an account's subscription to a peer, enabled or not.
func (d *TelegramClientDAO) FindSubscription(ctx context.Context, accountID, peerID int64) (*model.TelegramClientSubscription, error) {
	var row model.TelegramClientSubscription
	err := d.db.WithContext(ctx).Where("account_id = ? AND peer_id = ?", accountID, peerID).First(&row).Error
	return &row, err
}
func (d *TelegramClientDAO) UpdateSubscription(ctx context.Context, id int64, keywords string, enabled *bool) error {
	updates := map[string]interface{}{"keywords": keywords}
	if enabled != nil {
		updates["enabled"] = *enabled
	}
	return d.db.WithContext(ctx).Model(&model.TelegramClientSubscription{}).Where("id = ?", id).Updates(updates).Error
}
func (d *TelegramClientDAO) DeleteSubscription(ctx context.Context, id int64) error {
	return d.db.WithContext(ctx).Delete(&model.TelegramClientSubscription{}, id).Error
}

func (d *TelegramClientDAO) SaveMessage(ctx context.Context, row *model.TelegramClientMessage) error {
	if row.Body == nil && row.Text != "" {
		row.SetText(row.Text) // stored compressed
	}
	err := d.db.WithContext(ctx).Create(row).Error
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return nil
	}
	if err != nil {
		return err
	}
	now := row.MessageAt
	if row.SubscriptionID > 0 {
		_ = d.db.WithContext(ctx).Model(&model.TelegramClientSubscription{}).Where("id = ?", row.SubscriptionID).Update("last_message_at", &now).Error
	}
	return d.db.WithContext(ctx).Model(&model.TelegramClientAccount{}).Where("id = ?", row.AccountID).Update("last_message_at", &now).Error
}
func (d *TelegramClientDAO) ListMessages(ctx context.Context, req *apis.TelegramClientMessageListReq) ([]model.TelegramClientMessage, int64, error) {
	var rows []model.TelegramClientMessage
	db := d.db.WithContext(ctx).Model(&model.TelegramClientMessage{})
	if req.AccountID > 0 {
		db = db.Where("account_id = ?", req.AccountID)
	}
	if req.SubscriptionID > 0 {
		db = db.Where("subscription_id = ?", req.SubscriptionID)
	}
	if req.SourceType != "" {
		db = db.Where("source_type = ?", req.SourceType)
	}
	if req.Status != "" {
		db = db.Where("status = ?", req.Status)
	}
	// The text is stored compressed, so it cannot be searched; the sender and the
	// source can.
	if q := strings.TrimSpace(req.Keyword); q != "" {
		db = db.Where("sender_name LIKE ? OR source LIKE ?", "%"+q+"%", "%"+q+"%")
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	// Lists never load the bodies.
	err := db.Omit("body").Order("message_at DESC, id DESC").Offset(int(req.Offset())).Limit(int(req.PageSize)).Find(&rows).Error
	return rows, total, err
}

// GetMessage reads one cached message with its text decoded.
func (d *TelegramClientDAO) GetMessage(ctx context.Context, id int64) (*model.TelegramClientMessage, error) {
	var row model.TelegramClientMessage
	if err := d.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, err
	}
	if err := row.LoadText(); err != nil {
		return nil, err
	}
	return &row, nil
}

// DeleteMessages discards cached messages.
func (d *TelegramClientDAO) DeleteMessages(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := d.db.WithContext(ctx).Where("id IN ?", ids).Delete(&model.TelegramClientMessage{})
	return res.RowsAffected, res.Error
}

// RetryMessages puts messages back to pending so the forward job delivers them
// again to every target (earlier deliveries are forgotten).
func (d *TelegramClientDAO) RetryMessages(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := d.db.WithContext(ctx).Model(&model.TelegramClientMessage{}).Where("id IN ?", ids).Updates(map[string]any{
		"status": model.TelegramMsgPending, "attempts": 0, "next_attempt_at": nil, "done_rules": "", "last_error": "", "consumed_at": nil,
	})
	return res.RowsAffected, res.Error
}

// ConsumeMessages marks messages consumed without forwarding them; the cleanup
// job deletes them shortly after.
func (d *TelegramClientDAO) ConsumeMessages(ctx context.Context, ids []int64, now time.Time) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := d.db.WithContext(ctx).Model(&model.TelegramClientMessage{}).Where("id IN ?", ids).Updates(map[string]any{
		"status": model.TelegramMsgConsumed, "consumed_at": &now, "next_attempt_at": nil, "last_error": "",
	})
	return res.RowsAffected, res.Error
}

// FailMessage marks a message that can never be delivered (for example an
// unreadable body) failed at once.
func (d *TelegramClientDAO) FailMessage(ctx context.Context, id int64, message string) error {
	return d.db.WithContext(ctx).Model(&model.TelegramClientMessage{}).Where("id = ?", id).
		Updates(map[string]any{"status": model.TelegramMsgFailed, "last_error": message, "next_attempt_at": nil}).Error
}

// SetSubscriptionsEnabled switches subscriptions on or off.
func (d *TelegramClientDAO) SetSubscriptionsEnabled(ctx context.Context, ids []int64, enabled bool) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := d.db.WithContext(ctx).Model(&model.TelegramClientSubscription{}).Where("id IN ?", ids).Update("enabled", enabled)
	return res.RowsAffected, res.Error
}

// ---- message cache: consumption and clean-up (run by the TG Agent)

// ClaimDueMessages returns pending cached messages whose next attempt is due.
func (d *TelegramClientDAO) ClaimDueMessages(ctx context.Context, now time.Time, limit int) ([]model.TelegramClientMessage, error) {
	var rows []model.TelegramClientMessage
	err := d.db.WithContext(ctx).
		Where("status = ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)", model.TelegramMsgPending, now).
		Order("id ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// MarkMessageConsumed records that every matching rule delivered the message.
func (d *TelegramClientDAO) MarkMessageConsumed(ctx context.Context, id int64, doneRules string, now time.Time) error {
	return d.db.WithContext(ctx).Model(&model.TelegramClientMessage{}).Where("id = ?", id).Updates(map[string]any{
		"status": model.TelegramMsgConsumed, "consumed_at": &now, "done_rules": doneRules, "last_error": "", "next_attempt_at": nil,
	}).Error
}

// DeferMessage keeps a message pending and checks it again at next.
// A time already set further ahead (the spacing of a backlog) is kept.
func (d *TelegramClientDAO) DeferMessage(ctx context.Context, id int64, next time.Time) error {
	return d.db.WithContext(ctx).Model(&model.TelegramClientMessage{}).
		Where("id = ? AND (next_attempt_at IS NULL OR next_attempt_at < ?)", id, next).Update("next_attempt_at", &next).Error
}

// RetryMessage records a failed delivery. Once attempts reach maxAttempts the
// message becomes failed (kept until it expires) instead of being retried.
func (d *TelegramClientDAO) RetryMessage(ctx context.Context, id int64, attempts, maxAttempts uint8, next time.Time, doneRules, message string) error {
	updates := map[string]any{"attempts": attempts, "done_rules": doneRules, "last_error": message, "next_attempt_at": &next}
	if attempts >= maxAttempts {
		updates["status"] = model.TelegramMsgFailed
		updates["next_attempt_at"] = nil
	}
	return d.db.WithContext(ctx).Model(&model.TelegramClientMessage{}).Where("id = ?", id).Updates(updates).Error
}

// MessageStats counts the cache by state.
func (d *TelegramClientDAO) MessageStats(ctx context.Context) (map[string]int64, error) {
	var rows []struct {
		Status string
		N      int64
	}
	if err := d.db.WithContext(ctx).Model(&model.TelegramClientMessage{}).Select("status, COUNT(*) AS n").Group("status").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := map[string]int64{model.TelegramMsgPending: 0, model.TelegramMsgConsumed: 0, model.TelegramMsgFailed: 0}
	for _, row := range rows {
		out[row.Status] = row.N
	}
	return out, nil
}

// CleanMessages deletes consumed messages older than consumedBefore and any
// message that has been cached since before expiredBefore. It returns how many
// of each it removed.
func (d *TelegramClientDAO) CleanMessages(ctx context.Context, consumedBefore, expiredBefore time.Time) (consumed, expired int64, err error) {
	res := d.db.WithContext(ctx).Where("status = ? AND consumed_at < ?", model.TelegramMsgConsumed, consumedBefore).Delete(&model.TelegramClientMessage{})
	if res.Error != nil {
		return 0, 0, res.Error
	}
	consumed = res.RowsAffected
	res = d.db.WithContext(ctx).Where("created_at < ?", expiredBefore).Delete(&model.TelegramClientMessage{})
	return consumed, res.RowsAffected, res.Error
}
