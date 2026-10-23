package rdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"gorm.io/gorm"
	"sync"
	"time"
)

var (
	ErrBindingNotFound   = errors.New("telegram binding not found")
	ErrTokenExpiredOrBad = errors.New("binding token is invalid or expired")
	ErrUserAlreadyBound  = errors.New("this telegram account is already bound to another user")
)

type bindTokenEntry struct {
	userID    uint
	expiresAt time.Time
}

var (
	globalTokensMu   sync.RWMutex
	globalBindTokens = make(map[string]bindTokenEntry)
	cleanerOnce      sync.Once
)

// TelegramBindingDAO handles database operations and temporary token caching for Telegram bindings
type TelegramBindingDAO struct {
	db *gorm.DB
}

func NewTelegramBindingDAO(db *gorm.DB) *TelegramBindingDAO {
	dao := &TelegramBindingDAO{
		db: db,
	}
	cleanerOnce.Do(func() {
		go startGlobalTokenCleaner()
	})
	return dao
}

func startGlobalTokenCleaner() {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		globalTokensMu.Lock()
		now := time.Now()
		for token, entry := range globalBindTokens {
			if now.After(entry.expiresAt) {
				delete(globalBindTokens, token)
			}
		}
		globalTokensMu.Unlock()
	}
}

// GenerateBindToken creates a temporary token for linking PC user with Telegram
func (dao *TelegramBindingDAO) GenerateBindToken(ctx context.Context, userID uint, botUsername string) (*apis.TelegramBindGenerateResp, error) {
	var user model.User
	if err := dao.db.WithContext(ctx).First(&user, userID).Error; err != nil {
		return nil, fmt.Errorf("user not found: %w", err)
	}

	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	token := "bind_" + hex.EncodeToString(raw)
	expiresAt := time.Now().Add(5 * time.Minute)

	globalTokensMu.Lock()
	globalBindTokens[token] = bindTokenEntry{
		userID:    userID,
		expiresAt: expiresAt,
	}
	globalTokensMu.Unlock()

	deepLink := ""
	if botUsername != "" {
		deepLink = fmt.Sprintf("https://t.me/%s?start=%s", botUsername, token)
	}

	return &apis.TelegramBindGenerateResp{
		Token:     token,
		DeepLink:  deepLink,
		ExpiresAt: expiresAt,
	}, nil
}

// ConfirmBindWithToken links the user corresponding to the token with the given Telegram user
func (dao *TelegramBindingDAO) ConfirmBindWithToken(ctx context.Context, token string, tgUser *model.TelegramUser, authDate time.Time) (*model.TelegramBinding, error) {
	if tgUser == nil {
		return nil, errors.New("telegram user is nil")
	}

	globalTokensMu.Lock()
	entry, ok := globalBindTokens[token]
	if !ok || time.Now().After(entry.expiresAt) {
		globalTokensMu.Unlock()
		return nil, ErrTokenExpiredOrBad
	}
	// Invalidate token immediately after single use
	delete(globalBindTokens, token)
	globalTokensMu.Unlock()

	var existingWithTG model.TelegramBinding
	err := dao.db.WithContext(ctx).Where("telegram_user_id = ?", tgUser.ID).First(&existingWithTG).Error
	if err == nil && existingWithTG.UserID != entry.userID {
		return nil, ErrUserAlreadyBound
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// TelegramUserID's uniqueIndex isn't scoped to "deleted_at IS NULL",
		// so a soft-deleted row from an earlier Unbind of this same
		// Telegram account (by anyone) still occupies the unique slot even
		// though the scoped query above found nothing live. Purge it now so
		// the Create() below (new binding, different user_id than before)
		// doesn't fail on that unique constraint.
		if err := dao.db.WithContext(ctx).Unscoped().
			Where("telegram_user_id = ? AND deleted_at IS NOT NULL", tgUser.ID).
			Delete(&model.TelegramBinding{}).Error; err != nil {
			return nil, err
		}
	}

	var binding model.TelegramBinding
	err = dao.db.WithContext(ctx).Where("user_id = ?", entry.userID).First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		binding = model.TelegramBinding{
			UserID:           entry.userID,
			TelegramUserID:   tgUser.ID,
			TelegramUsername: tgUser.Username,
			FirstName:        tgUser.FirstName,
			LastName:         tgUser.LastName,
			PhotoURL:         tgUser.PhotoURL,
			AuthDate:         authDate,
			Status:           true,
		}
		if err := dao.db.WithContext(ctx).Create(&binding).Error; err != nil {
			return nil, err
		}
	} else if err == nil {
		updates := map[string]interface{}{
			"telegram_user_id":  tgUser.ID,
			"telegram_username": tgUser.Username,
			"first_name":        tgUser.FirstName,
			"last_name":         tgUser.LastName,
			"photo_url":         tgUser.PhotoURL,
			"auth_date":         authDate,
			"status":            true,
		}
		if err := dao.db.WithContext(ctx).Model(&binding).Updates(updates).Error; err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}

	// Reload with User
	if err := dao.db.WithContext(ctx).Preload("User.Roles.Permissions").First(&binding, binding.ID).Error; err != nil {
		return nil, err
	}
	return &binding, nil
}

// GetBindingByTelegramID finds active binding and preloads User
func (dao *TelegramBindingDAO) GetBindingByTelegramID(ctx context.Context, telegramUserID int64) (*model.TelegramBinding, error) {
	var binding model.TelegramBinding
	err := dao.db.WithContext(ctx).
		Where("telegram_user_id = ? AND status = ?", telegramUserID, true).
		Preload("User.Roles.Permissions").
		First(&binding).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBindingNotFound
		}
		return nil, err
	}
	return &binding, nil
}

// GetBindingByUserID finds active binding by KubeForecast user ID
func (dao *TelegramBindingDAO) GetBindingByUserID(ctx context.Context, userID uint) (*model.TelegramBinding, error) {
	var binding model.TelegramBinding
	err := dao.db.WithContext(ctx).
		Where("user_id = ? AND status = ?", userID, true).
		First(&binding).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBindingNotFound
		}
		return nil, err
	}
	return &binding, nil
}

// CreateOrUpdateMockBinding links userID to a Telegram user without going
// through the real bind-token flow. Used only by the local/dev mock login
// path, which always uses the same hardcoded mock Telegram identity (see
// TelegramRouter.MockLogin) — so the lookup key must be TelegramUserID (the
// column with the real uniqueIndex), not userID: whichever KubeForecast user was
// "first active" the last time mock-login ran may not be the same one
// today (e.g. after reseeding/reordering admin accounts), and looking up by
// userID would then miss the existing row and crash on the unique
// constraint when trying to re-insert the same TelegramUserID.
//
// The lookup is Unscoped because TelegramUserID's uniqueIndex isn't scoped
// to "deleted_at IS NULL" — a soft-deleted row (e.g. left over from an
// earlier Unbind) still occupies that unique slot in the DB and would make
// a plain Create() below fail with a unique-constraint error even though it
// looks like nothing is bound. Reviving/re-targeting the row found this way
// is what mock login actually wants: whoever is first-active now.
func (dao *TelegramBindingDAO) CreateOrUpdateMockBinding(ctx context.Context, userID uint, tgUser *model.TelegramUser) (*model.TelegramBinding, error) {
	var binding model.TelegramBinding
	err := dao.db.WithContext(ctx).Unscoped().Where("telegram_user_id = ?", tgUser.ID).First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		binding = model.TelegramBinding{
			UserID:           userID,
			TelegramUserID:   tgUser.ID,
			TelegramUsername: tgUser.Username,
			FirstName:        tgUser.FirstName,
			LastName:         tgUser.LastName,
			Status:           true,
			AuthDate:         time.Now(),
		}
		if err := dao.db.WithContext(ctx).Create(&binding).Error; err != nil {
			return nil, err
		}
		return &binding, nil
	}
	if err != nil {
		return nil, err
	}
	err = dao.db.WithContext(ctx).Unscoped().Model(&binding).Updates(map[string]interface{}{
		"user_id":           userID,
		"telegram_username": tgUser.Username,
		"first_name":        tgUser.FirstName,
		"status":            true,
		"deleted_at":        nil,
	}).Error
	return &binding, err
}

// Unbind removes or disables Telegram binding for the user
func (dao *TelegramBindingDAO) Unbind(ctx context.Context, userID uint) error {
	return dao.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Delete(&model.TelegramBinding{}).Error
}

// ListActive returns every active binding, for fanning a notification out to
// all backend users who have bound their personal Telegram chat.
func (dao *TelegramBindingDAO) ListActive(ctx context.Context) ([]model.TelegramBinding, error) {
	var bindings []model.TelegramBinding
	err := dao.db.WithContext(ctx).Where("status = ?", true).Find(&bindings).Error
	return bindings, err
}

// ListActiveAdmins returns every active binding whose KubeForecast user holds the
// ADMIN role — used to fan an approval card out to
// exactly the people who are allowed to act on it (see
// pkg/telegram/bot.go's free-text workflow handler).
func (dao *TelegramBindingDAO) ListActiveAdmins(ctx context.Context) ([]model.TelegramBinding, error) {
	return dao.ListActiveByRole(ctx, "ADMIN")
}

// ListActiveByRole returns every active binding whose KubeForecast user holds the
// given role code, used to fan out workflow notifications to exactly the
// people allowed to act on a given step.
func (dao *TelegramBindingDAO) ListActiveByRole(ctx context.Context, roleCode string) ([]model.TelegramBinding, error) {
	var bindings []model.TelegramBinding
	err := dao.db.WithContext(ctx).
		Preload("User").
		Distinct("telegram_bindings.*").
		Joins("JOIN sys_user_roles ON sys_user_roles.user_id = telegram_bindings.user_id").
		Joins("JOIN roles ON roles.id = sys_user_roles.role_id").
		Where("telegram_bindings.status = ? AND roles.code = ? AND roles.status = ?", true, roleCode, true).
		Find(&bindings).Error
	return bindings, err
}

// ListPaginated returns all bindings (active or not) with the owning user
// preloaded, for the admin bindings management page.
func (dao *TelegramBindingDAO) ListPaginated(ctx context.Context, page, pageSize int) ([]model.TelegramBinding, int64, error) {
	page, pageSize = normalizePage(page, pageSize)
	var total int64
	if err := dao.db.WithContext(ctx).Model(&model.TelegramBinding{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var bindings []model.TelegramBinding
	err := dao.db.WithContext(ctx).
		Preload("User").
		Order("id DESC").
		Offset(pageOffset(page, pageSize)).
		Limit(pageSize).
		Find(&bindings).Error
	return bindings, total, err
}

// AdminUnbind removes a binding by its own ID, for admin-initiated unbinding
// of any user's account (distinct from Unbind, which is self-service by the
// bound user's own userID).
func (dao *TelegramBindingDAO) AdminUnbind(ctx context.Context, id int64) error {
	return dao.db.WithContext(ctx).Delete(&model.TelegramBinding{}, id).Error
}

// BatchAdminUnbind force-removes multiple Telegram bindings by their record IDs.
func (dao *TelegramBindingDAO) BatchAdminUnbind(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	return dao.db.WithContext(ctx).Where("id IN ?", ids).Delete(&model.TelegramBinding{}).Error
}
