package rdb

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"gorm.io/gorm"
	"regexp"
	"strings"
)

var telegramRecipient = regexp.MustCompile(`^(-?\d{1,20}|@[A-Za-z][A-Za-z0-9_]{3,31})$`)

type TelegramForwardRuleDAO struct{ db *gorm.DB }

func NewTelegramForwardRuleDAO(db *gorm.DB) *TelegramForwardRuleDAO {
	return &TelegramForwardRuleDAO{db: db}
}

type TelegramForwardRuleFilter struct {
	Keyword        string
	TargetClass    string
	AccountID      int64
	SubscriptionID int64
	SourceType     string
	TargetKind     string
	Enabled        *bool
}

// Validate normalizes and checks a rule.
func (r *TelegramForwardRuleDAO) Validate(row *model.TelegramForwardRule) error {
	row.Name = strings.TrimSpace(row.Name)
	row.Pattern = strings.TrimSpace(row.Pattern)
	row.TargetKind = strings.ToLower(strings.TrimSpace(row.TargetKind))
	row.TargetRef = strings.TrimSpace(row.TargetRef)
	row.TargetRecipient = strings.TrimSpace(row.TargetRecipient)
	if row.Name == "" {
		return invalidInput("name is required")
	}
	if row.Pattern != "" {
		if _, err := regexp.Compile(row.Pattern); err != nil {
			return invalidInput("pattern is not a valid regular expression")
		}
	}
	row.SourceType = strings.ToLower(strings.TrimSpace(row.SourceType))
	switch row.SourceType {
	case "", model.TelegramSourceChannel, model.TelegramSourceGroup, model.TelegramSourcePrivate, model.TelegramSourceSystem:
	default:
		return invalidInput("source_type must be channel, group, private or system")
	}
	if row.AccountID < 0 {
		return invalidInput("invalid account")
	}
	switch row.TargetKind {
	case model.ForwardTargetNotify:
		if row.TargetRef == "" || row.TargetRecipient == "" {
			return invalidInput("a notify target needs a channel and a recipient")
		}
		if strings.EqualFold(row.TargetRef, "telegram") && !telegramRecipient.MatchString(row.TargetRecipient) {
			return invalidInput("a Telegram recipient must be a numeric chat id (a user who has started the bot) or the @username of a channel or group the bot can post to")
		}
	case model.ForwardTargetChannel:
		if row.ChannelID <= 0 {
			return invalidInput("choose the forward channel the message is written to")
		}
	default:
		return invalidInput("target_kind must be notify or channel")
	}
	if row.SubscriptionID < 0 {
		return invalidInput("invalid subscription")
	}
	if row.SendAccountID < 0 {
		return invalidInput("invalid sending account")
	}
	if row.TargetKind != model.ForwardTargetNotify {
		row.SendAccountID = 0
		row.TargetRecipient = ""
	} else {
		row.ChannelID = 0
	}
	return nil
}

// resolveChannel checks the channel of a task rule exists and records its name as the rule's target.
func (r *TelegramForwardRuleDAO) resolveChannel(ctx context.Context, row *model.TelegramForwardRule) error {
	if row.TargetKind != model.ForwardTargetChannel {
		return nil
	}
	var channel model.TelegramForwardChannel
	if err := r.db.WithContext(ctx).First(&channel, row.ChannelID).Error; err != nil {
		return invalidInput("the forward channel does not exist")
	}
	row.TargetRef = channel.Name
	return nil
}

func (r *TelegramForwardRuleDAO) Create(ctx context.Context, row *model.TelegramForwardRule) error {
	if err := r.Validate(row); err != nil {
		return err
	}
	if err := r.resolveChannel(ctx, row); err != nil {
		return err
	}
	enabled := row.Enabled
	if err := r.db.WithContext(ctx).Create(row).Error; err != nil {
		return err
	}
	// Enabled has a database default of true, so GORM inserts true in place of
	// false; store the requested value explicitly.
	row.TargetClass = model.ForwardClassOf(row.TargetKind)
	if !enabled {
		row.Enabled = false
		return r.db.WithContext(ctx).Model(row).UpdateColumn("enabled", false).Error
	}
	return nil
}

// Update replaces a rule with the merged result of the stored row and patch.
func (r *TelegramForwardRuleDAO) Update(ctx context.Context, id int64, patch model.TelegramForwardRule) (*model.TelegramForwardRule, error) {
	stored, err := r.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if patch.Name == "" {
		patch.Name = stored.Name
	}
	if patch.TargetKind == "" {
		patch.TargetKind = stored.TargetKind
	}
	if patch.TargetRef == "" {
		patch.TargetRef = stored.TargetRef
	}
	if patch.TargetKind == model.ForwardTargetChannel && patch.ChannelID == 0 {
		patch.ChannelID = stored.ChannelID
	}
	if err := r.Validate(&patch); err != nil {
		return nil, err
	}
	if err := r.resolveChannel(ctx, &patch); err != nil {
		return nil, err
	}
	patch.ID, patch.CreatedAt = stored.ID, stored.CreatedAt
	patch.TargetClass = model.ForwardClassOf(patch.TargetKind)
	if err := r.db.WithContext(ctx).Save(&patch).Error; err != nil {
		return nil, err
	}
	return &patch, nil
}

func (r *TelegramForwardRuleDAO) Get(ctx context.Context, id int64) (*model.TelegramForwardRule, error) {
	var row model.TelegramForwardRule
	if err := r.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *TelegramForwardRuleDAO) List(ctx context.Context, page, pageSize int, f TelegramForwardRuleFilter) ([]model.TelegramForwardRule, int64, error) {
	page, pageSize = pageBounds(page, pageSize)
	db := r.db.WithContext(ctx).Model(&model.TelegramForwardRule{})
	if f.Keyword != "" {
		db = db.Where("name LIKE ? OR target_ref LIKE ?", like(f.Keyword), like(f.Keyword))
	}
	if f.AccountID > 0 {
		db = db.Where("account_id = ?", f.AccountID)
	}
	if f.SubscriptionID > 0 {
		db = db.Where("subscription_id = ?", f.SubscriptionID)
	}
	if f.SourceType != "" {
		db = db.Where("source_type = ?", f.SourceType)
	}
	if f.TargetKind != "" {
		db = db.Where("target_kind = ?", f.TargetKind)
	}
	if f.TargetClass != "" {
		db = db.Where("target_kind IN ?", model.ForwardKindsOf(f.TargetClass))
	}
	if f.Enabled != nil {
		db = db.Where("enabled = ?", *f.Enabled)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.TelegramForwardRule
	err := db.Order("id ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error
	return rows, total, err
}

// ForSource returns the enabled rules that apply to a message: rules for every
// account / subscription / source type, plus the ones that name this one.
func (r *TelegramForwardRuleDAO) ForSource(ctx context.Context, accountID, subscriptionID int64, sourceType string) ([]model.TelegramForwardRule, error) {
	var rows []model.TelegramForwardRule
	err := r.db.WithContext(ctx).
		Where("enabled = ? AND account_id IN ? AND subscription_id IN ? AND source_type IN ?", true,
			[]int64{0, accountID}, []int64{0, subscriptionID}, []string{"", sourceType}).
		Order("id ASC").Find(&rows).Error
	return rows, err
}

func (r *TelegramForwardRuleDAO) SetEnabled(ctx context.Context, ids []int64, enabled bool) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := r.db.WithContext(ctx).Model(&model.TelegramForwardRule{}).Where("id IN ?", ids).Update("enabled", enabled)
	return res.RowsAffected, res.Error
}

func (r *TelegramForwardRuleDAO) Delete(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := r.db.WithContext(ctx).Where("id IN ?", ids).Delete(&model.TelegramForwardRule{})
	return res.RowsAffected, res.Error
}
