package rdb

import (
	"context"
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/model"
	cryptoutils "github.com/cylonchau/kubeforecast/pkg/utils"
	"gorm.io/gorm"
	"regexp"
	"strings"
	"time"
)

// telegramBotFreshness is how recently the Telegram Agent must have reported
// a bot for the API to call it connected.
const telegramBotFreshness = 90 * time.Second

var botTokenPattern = regexp.MustCompile(`^\d{3,}:[A-Za-z0-9_-]{20,}$`)

// InvalidInputError marks a rejected value, so callers can answer 400 instead
// of treating it as a storage failure.
type InvalidInputError struct{ Msg string }

func (e InvalidInputError) Error() string { return e.Msg }

func invalidInput(msg string) error { return InvalidInputError{Msg: msg} }

type TelegramBotDAO struct{ db *gorm.DB }

func NewTelegramBotDAO(db *gorm.DB) *TelegramBotDAO { return &TelegramBotDAO{db: db} }

// TelegramBotInput is the writable part of a bot. Nil pointers leave a field
// unchanged on update; an empty Token keeps the stored one.
type TelegramBotInput struct {
	Name       *string
	Username   *string
	Token      *string
	Mode       *string
	APIBaseURL *string
	MiniAppURL *string
	Enabled    *bool
}

type TelegramBotFilter struct {
	Keyword string
	Enabled *bool
	Status  string
}

func decorateBot(row *model.TelegramBot, now time.Time) {
	row.HasToken = row.TokenEncrypted != ""
	row.Connected = row.Status == model.TelegramBotRunning && row.SeenAt != nil && now.Sub(*row.SeenAt) <= telegramBotFreshness
}

func cleanMode(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "", model.TelegramModePolling:
		return model.TelegramModePolling, nil
	case model.TelegramModeWebhook:
		return "", invalidInput("webhook mode is not supported yet; use polling")
	}
	return "", invalidInput("unknown connection mode")
}

func (d *TelegramBotDAO) Create(ctx context.Context, in TelegramBotInput) (*model.TelegramBot, error) {
	row := &model.TelegramBot{Enabled: true, Status: model.TelegramBotStopped}
	if in.Name == nil || strings.TrimSpace(*in.Name) == "" {
		return nil, invalidInput("name is required")
	}
	row.Name = strings.TrimSpace(*in.Name)
	if in.Token == nil || !botTokenPattern.MatchString(strings.TrimSpace(*in.Token)) {
		return nil, invalidInput("a valid bot token is required")
	}
	encrypted, err := cryptoutils.EncryptValue(strings.TrimSpace(*in.Token))
	if err != nil {
		return nil, err
	}
	row.TokenEncrypted = encrypted
	mode := ""
	if in.Mode != nil {
		mode = *in.Mode
	}
	if row.Mode, err = cleanMode(mode); err != nil {
		return nil, err
	}
	if in.Username != nil {
		row.Username = strings.TrimPrefix(strings.TrimSpace(*in.Username), "@")
	}
	if in.APIBaseURL != nil {
		row.APIBaseURL = strings.TrimSpace(*in.APIBaseURL)
	}
	if in.MiniAppURL != nil {
		row.MiniAppURL = strings.TrimSpace(*in.MiniAppURL)
	}
	if in.Enabled != nil {
		row.Enabled = *in.Enabled
	}
	enabled := row.Enabled
	if err := d.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, err
	}
	// Enabled has a database default of true, so GORM inserts true in place of
	// false; store the requested value explicitly.
	if !enabled {
		if err := d.db.WithContext(ctx).Model(row).UpdateColumn("enabled", false).Error; err != nil {
			return nil, err
		}
		row.Enabled = false
	}
	decorateBot(row, time.Now())
	return row, nil
}

func (d *TelegramBotDAO) Update(ctx context.Context, id int64, in TelegramBotInput) (*model.TelegramBot, error) {
	updates := map[string]any{}
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return nil, invalidInput("name cannot be empty")
		}
		updates["name"] = name
	}
	if in.Username != nil {
		updates["username"] = strings.TrimPrefix(strings.TrimSpace(*in.Username), "@")
	}
	if in.Token != nil && strings.TrimSpace(*in.Token) != "" {
		token := strings.TrimSpace(*in.Token)
		if !botTokenPattern.MatchString(token) {
			return nil, invalidInput("the bot token is not valid")
		}
		encrypted, err := cryptoutils.EncryptValue(token)
		if err != nil {
			return nil, err
		}
		updates["token_encrypted"] = encrypted
		updates["commands_hash"] = ""
	}
	if in.Mode != nil {
		mode, err := cleanMode(*in.Mode)
		if err != nil {
			return nil, err
		}
		updates["mode"] = mode
	}
	if in.APIBaseURL != nil {
		updates["api_base_url"] = strings.TrimSpace(*in.APIBaseURL)
	}
	if in.MiniAppURL != nil {
		updates["mini_app_url"] = strings.TrimSpace(*in.MiniAppURL)
	}
	if in.Enabled != nil {
		updates["enabled"] = *in.Enabled
	}
	if len(updates) > 0 {
		res := d.db.WithContext(ctx).Model(&model.TelegramBot{}).Where("id = ?", id).Updates(updates)
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected == 0 {
			return nil, gorm.ErrRecordNotFound
		}
	}
	return d.Get(ctx, id)
}

func (d *TelegramBotDAO) Get(ctx context.Context, id int64) (*model.TelegramBot, error) {
	var row model.TelegramBot
	if err := d.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, err
	}
	decorateBot(&row, time.Now())
	return &row, nil
}

func (d *TelegramBotDAO) List(ctx context.Context, page, pageSize int, f TelegramBotFilter) ([]model.TelegramBot, int64, error) {
	page, pageSize = pageBounds(page, pageSize)
	db := d.db.WithContext(ctx).Model(&model.TelegramBot{})
	if f.Keyword != "" {
		db = db.Where("name LIKE ? OR username LIKE ?", like(f.Keyword), like(f.Keyword))
	}
	if f.Enabled != nil {
		db = db.Where("enabled = ?", *f.Enabled)
	}
	if f.Status != "" {
		db = db.Where("status = ?", f.Status)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.TelegramBot
	if err := db.Order("id ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	now := time.Now()
	for i := range rows {
		decorateBot(&rows[i], now)
	}
	return rows, total, nil
}

// ListEnabled returns the bots the Telegram Agent should be running.
func (d *TelegramBotDAO) ListEnabled(ctx context.Context) ([]model.TelegramBot, error) {
	var rows []model.TelegramBot
	err := d.db.WithContext(ctx).Where("enabled = ?", true).Order("id ASC").Find(&rows).Error
	return rows, err
}

func (d *TelegramBotDAO) Token(row *model.TelegramBot) (string, error) {
	return cryptoutils.DecryptValue(row.TokenEncrypted)
}

func (d *TelegramBotDAO) SetEnabled(ctx context.Context, ids []int64, enabled bool) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := d.db.WithContext(ctx).Model(&model.TelegramBot{}).Where("id IN ?", ids).Update("enabled", enabled)
	return res.RowsAffected, res.Error
}

// Delete removes bots together with the commands bound to them.
func (d *TelegramBotDAO) Delete(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var deleted int64
	err := d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("bot_id IN ?", ids).Delete(&model.BotCommandDef{}).Error; err != nil {
			return err
		}
		res := tx.Where("id IN ?", ids).Delete(&model.TelegramBot{})
		deleted = res.RowsAffected
		return res.Error
	})
	return deleted, err
}

// MarkRunning records a healthy bot. username and botUserID come from getMe.
func (d *TelegramBotDAO) MarkRunning(ctx context.Context, id int64, username string, botUserID int64) error {
	now := time.Now()
	updates := map[string]any{"status": model.TelegramBotRunning, "last_error": "", "seen_at": &now, "started_at": &now, "bot_user_id": botUserID}
	if username != "" {
		updates["username"] = username
	}
	return d.db.WithContext(ctx).Model(&model.TelegramBot{}).Where("id = ?", id).Updates(updates).Error
}

// Touch refreshes the heartbeat of running bots.
func (d *TelegramBotDAO) Touch(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now()
	return d.db.WithContext(ctx).Model(&model.TelegramBot{}).Where("id IN ? AND status = ?", ids, model.TelegramBotRunning).Update("seen_at", &now).Error
}

func (d *TelegramBotDAO) MarkStopped(ctx context.Context, id int64) error {
	return d.db.WithContext(ctx).Model(&model.TelegramBot{}).Where("id = ?", id).Updates(map[string]any{"status": model.TelegramBotStopped, "started_at": nil}).Error
}

func (d *TelegramBotDAO) MarkError(ctx context.Context, id int64, message string) error {
	return d.db.WithContext(ctx).Model(&model.TelegramBot{}).Where("id = ?", id).Updates(map[string]any{"status": model.TelegramBotError, "last_error": message}).Error
}

func (d *TelegramBotDAO) MarkCommandsSynced(ctx context.Context, id int64, hash string) error {
	now := time.Now()
	return d.db.WithContext(ctx).Model(&model.TelegramBot{}).Where("id = ?", id).Updates(map[string]any{"commands_hash": hash, "commands_synced_at": &now}).Error
}

// ClearCommandsHash forces the next reconcile to push the command list again.
func (d *TelegramBotDAO) ClearCommandsHash(ctx context.Context, id int64) error {
	return d.db.WithContext(ctx).Model(&model.TelegramBot{}).Where("id = ?", id).Update("commands_hash", "").Error
}

// CommandsFor returns the enabled commands a bot exposes: its own plus the
// ones bound to every bot (bot_id 0), bot-specific rows winning by command.
func (d *TelegramBotDAO) CommandsFor(ctx context.Context, botID int64) ([]model.BotCommandDef, error) {
	var rows []model.BotCommandDef
	err := d.db.WithContext(ctx).Where("enabled = ? AND bot_id IN ?", true, []int64{0, botID}).Order("bot_id DESC, id ASC").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]model.BotCommandDef, 0, len(rows))
	for _, row := range rows {
		if seen[row.Command] {
			continue
		}
		seen[row.Command] = true
		out = append(out, row)
	}
	return out, nil
}

// CommandCounts returns the number of commands each bot exposes.
func (d *TelegramBotDAO) CommandCounts(ctx context.Context, bots []model.TelegramBot) map[int64]int64 {
	counts := make(map[int64]int64, len(bots))
	for _, bot := range bots {
		if rows, err := d.CommandsFor(ctx, bot.ID); err == nil {
			counts[bot.ID] = int64(len(rows))
		}
	}
	return counts
}

// DefaultSender returns the credentials of the first enabled bot, used by the
// Notify Telegram provider. ok is false when no bot is configured.
func (d *TelegramBotDAO) DefaultSender(ctx context.Context) (token, apiBaseURL, miniAppURL string, ok bool, err error) {
	var row model.TelegramBot
	if err = d.db.WithContext(ctx).Where("enabled = ?", true).Order("id ASC").First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", "", false, nil
		}
		return "", "", "", false, err
	}
	token, err = d.Token(&row)
	if err != nil {
		return "", "", "", false, err
	}
	return token, row.APIBaseURL, row.MiniAppURL, true, nil
}
