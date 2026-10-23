package rdb

import (
	"context"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strconv"
	"strings"
	"sync"
	"time"
)

// sysConfigCacheTTL bounds how stale a cached config read can be — long
// enough to collapse the repeated GetByKey calls a single notify broadcast
// or send makes (see pkg/job/tasks/notify.go), short enough that an admin
// editing a value in the config UI sees it take effect almost immediately
// even without the explicit invalidation UpdateValue/Create already do.
const sysConfigCacheTTL = 10 * time.Second

type sysConfigCacheEntry struct {
	value     *model.SysConfig
	expiresAt time.Time
}

type SysConfigDAO struct {
	db *gorm.DB

	cacheMu sync.RWMutex
	cache   map[string]sysConfigCacheEntry
}

func NewSysConfigDAO(db *gorm.DB) *SysConfigDAO {
	return &SysConfigDAO{db: db, cache: make(map[string]sysConfigCacheEntry)}
}

// GetByKey queries config item by key, short-TTL cached per DAO instance —
// this DAO is long-lived (constructed once at startup and shared by every
// caller in a process), so the cache persists across requests/job runs.
func (dao *SysConfigDAO) GetByKey(ctx context.Context, key string) (*model.SysConfig, error) {
	dao.cacheMu.RLock()
	entry, ok := dao.cache[key]
	dao.cacheMu.RUnlock()
	if ok && time.Now().Before(entry.expiresAt) {
		return entry.value, nil
	}

	if IsMarkerKey(key) {
		value, found, err := GetMarker(dao.db.WithContext(ctx), key)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, gorm.ErrRecordNotFound
		}
		return &model.SysConfig{ConfigKey: key, ConfigValue: value}, nil
	}

	var config model.SysConfig
	err := dao.db.WithContext(ctx).Where("config_key = ?", key).First(&config).Error
	if err != nil {
		return nil, err
	}
	dao.cacheMu.Lock()
	dao.cache[key] = sysConfigCacheEntry{value: &config, expiresAt: time.Now().Add(sysConfigCacheTTL)}
	dao.cacheMu.Unlock()
	return &config, nil
}

// GetValue is GetByKey trimmed down to just the string value, for callers
// that want the raw ErrRecordNotFound/DB error surfaced rather than a
// fallback (see GetValueOrDefault).
func (dao *SysConfigDAO) GetValue(ctx context.Context, key string) (string, error) {
	cfg, err := dao.GetByKey(ctx, key)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(cfg.ConfigValue), nil
}

// GetValueOrDefault is GetValue with fallback substituted for both a missing
// row (gorm.ErrRecordNotFound) and an explicitly empty stored value — the
// shared shape every notify.*/expiry-check SysConfig read in pkg/job/tasks
// and pkg/notify/telegram uses, so the ErrRecordNotFound/empty-string
// handling lives in exactly one place.
func (dao *SysConfigDAO) GetValueOrDefault(ctx context.Context, key, fallback string) (string, error) {
	cfg, err := dao.GetByKey(ctx, key)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return fallback, nil
		}
		return "", err
	}
	if strings.TrimSpace(cfg.ConfigValue) == "" {
		return fallback, nil
	}
	return cfg.ConfigValue, nil
}

// GetBool is GetValueOrDefault parsed as a bool.
func (dao *SysConfigDAO) GetBool(ctx context.Context, key string, fallback bool) (bool, error) {
	value, err := dao.GetValueOrDefault(ctx, key, strconv.FormatBool(fallback))
	if err != nil {
		return false, err
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, fmt.Errorf("invalid boolean config %s=%q", key, value)
	}
	return parsed, nil
}

// invalidate drops a key's cached value, called by every write path so a
// config edit is visible immediately rather than waiting out the TTL.
func (dao *SysConfigDAO) invalidate(key string) {
	dao.cacheMu.Lock()
	delete(dao.cache, key)
	dao.cacheMu.Unlock()
}

// UpdateValue updates config item value
func (dao *SysConfigDAO) UpdateValue(ctx context.Context, key string, value string) error {
	if IsMarkerKey(key) {
		err := SetMarker(dao.db.WithContext(ctx), key, value)
		if err == nil {
			dao.invalidate(key)
		}
		return err
	}
	err := dao.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "config_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"config_value", "updated_at"}),
	}).Create(&model.SysConfig{
		ConfigKey:   key,
		ConfigValue: value,
		ConfigName:  key,
	}).Error
	if err == nil {
		dao.invalidate(key)
	}
	return err
}

// List fetches all config items
func (dao *SysConfigDAO) List(ctx context.Context) ([]model.SysConfig, error) {
	var configs []model.SysConfig
	err := dao.db.WithContext(ctx).Find(&configs).Error
	return configs, err
}

// Create creates a new config item
func (dao *SysConfigDAO) Create(ctx context.Context, config *model.SysConfig) error {
	if IsMarkerKey(config.ConfigKey) {
		err := SetMarker(dao.db.WithContext(ctx), config.ConfigKey, config.ConfigValue)
		if err == nil {
			dao.invalidate(config.ConfigKey)
		}
		return err
	}
	err := dao.db.WithContext(ctx).Create(config).Error
	if err == nil {
		dao.invalidate(config.ConfigKey)
	}
	return err
}
