package rdb

import (
	"context"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"regexp"
	"strings"
)

var seededKey = regexp.MustCompile(`(^|[._])seeded($|[._])`)
var datasetVersionKey = regexp.MustCompile(`^scanner\.labelsource\.[^.]+\.version$`)

// IsMarkerKey says whether a key is bookkeeping of the program (a migration or setup that ran, a collector's last look, a dataset
// version) and not a system parameter. Such keys live in system_marker; SysConfigDAO routes them there, so code that asks for them
// through the settings keeps working.
func IsMarkerKey(key string) bool {
	return strings.HasPrefix(key, "migration.") || strings.HasPrefix(key, "collector.") || strings.HasPrefix(key, "job.") ||
		seededKey.MatchString(key) || strings.HasSuffix(key, ".discovered_at") || datasetVersionKey.MatchString(key)
}

// GetMarker reads a marker; found is false when it was never written.
func GetMarker(db *gorm.DB, key string) (value string, found bool, err error) {
	var row model.SystemMarker
	res := db.Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: key}).Limit(1).Find(&row)
	if res.Error != nil {
		return "", false, res.Error
	}
	return row.Value, res.RowsAffected > 0, nil
}

// SetMarker writes a marker (replacing what was there).
func SetMarker(db *gorm.DB, key, value string) error {
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&model.SystemMarker{Key: key, Value: value}).Error
}

// CountMarker sets *out to 1 when the marker exists and to 0 when it does not: the shape of the "did this already run" checks.
func CountMarker(db *gorm.DB, key string, out *int64) error {
	_, found, err := GetMarker(db, key)
	*out = 0
	if found {
		*out = 1
	}
	return err
}

// MoveMarkers moves the bookkeeping rows that older versions kept among the system parameters into system_marker and deletes them from
// the parameters. It is safe to repeat.
func MoveMarkers(ctx context.Context, db *gorm.DB) (int, error) {
	if !db.Migrator().HasTable(&model.SysConfig{}) || !db.Migrator().HasTable(&model.SystemMarker{}) {
		return 0, nil
	}
	var rows []model.SysConfig
	if err := db.WithContext(ctx).Unscoped().Find(&rows).Error; err != nil {
		return 0, err
	}
	moved := 0
	for _, row := range rows {
		if !IsMarkerKey(row.ConfigKey) {
			continue
		}
		if err := SetMarker(db.WithContext(ctx), row.ConfigKey, row.ConfigValue); err != nil {
			return moved, err
		}
		if err := db.WithContext(ctx).Unscoped().Where("config_key = ?", row.ConfigKey).Delete(&model.SysConfig{}).Error; err != nil {
			return moved, err
		}
		moved++
	}
	return moved, nil
}
