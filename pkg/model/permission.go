package model

import (
	"gorm.io/gorm"
	"strings"
	"time"
)

// retiredMenuCodes are menus that were split or removed. A database that still holds them has them deleted by the
// upgrade after their grants moved to the successors, and they never route.
var retiredMenuCodes = map[string]bool{
	"forecast_history": true, "forecast_evaluation": true, "forecast_recommendations": true, "forecast_metrics": true,
	"authorization_ldap": true, "forecast_modules": true, "forecast_algorithm-templates": true, "forecast_input-sets": true,
	"forecast_catalog": true, "forecast_sources": true, "forecast-modules_bindings": true,
	"forecast_scores": true, "forecast-modules_sources": true,
	// the audit export and the separate history of the ranking were removed; the ranking page covers the latter
	"forecast-actions_audit-exports": true, "forecast_ranking-history": true, "forecast-results_trends": true, "forecast-results_backtests": true, "monitoring_metrics": true,
	// the training records open over the algorithm run status, the snapshot history over the resource recommendation
	"forecast-results_runs": true, "forecast-actions_snapshots": true,
}

// retiredButtonCodes are API permissions of removed features; the upgrade deletes them with their grants.
var retiredButtonCodes = map[string]bool{"forecast:audit:export": true}

// IsRetiredPermission reports whether a permission belongs to a removed feature.
func IsRetiredPermission(code string) bool { return retiredMenuCodes[code] || retiredButtonCodes[code] }

// IsPlatformRoute limits navigation to the supported modules and the hidden profile page.
func IsPlatformRoute(code string) bool {
	if code == "user-center" {
		return true
	}
	if code == "system_coins" || retiredMenuCodes[code] {
		return false
	}
	for _, module := range []string{"system", "authorization", "task-center", "datasource", "forecast", "forecast-modules", "forecast-results", "forecast-actions", "monitoring"} {
		if code == module || strings.HasPrefix(code, module+"_") {
			return true
		}
	}
	return false
}

func init() {
	RegisterModel(&Permission{})
}

type Permission struct {
	ID        uint           `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time      `json:"createdAt" column_setting:"true"`
	UpdatedAt time.Time      `json:"updatedAt" column_setting:"true"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-" column_setting:"false"`
	Name      string         `gorm:"type:varchar(50);not null" json:"name" column_setting:"true"`
	Code      string         `gorm:"type:varchar(100);not null;uniqueIndex" json:"code" column_setting:"true"` // e.g., system:user:view
	Type      string         `gorm:"type:varchar(10);not null" json:"type" column_setting:"true"`              // MENU / BUTTON
	// ParentID intentionally has no FK constraint (self-referential:
	// Permission -> Permission). 0 is this codebase's own "top-level menu,
	// no parent" sentinel, used at every seed site (see handle_route.go,
	// migration.go) and compared directly (ParentID > 0 / == 0) throughout
	// handle_route.go — the same class of conflict as Record.View. A real
	// FK (CASCADE or SET NULL) needs the column to be nullable, since a
	// standard SQL FK check doesn't exempt 0, only NULL; making that change
	// would mean converting to *uint/NULL across this model, the
	// parent_id/parentId API DTOs the frontend deserializes, and a data
	// migration for existing rows — deferred as out of scope for now.
	// Orphan cleanup for stale codes remains the manual Unscoped().Delete
	// calls in migration.go.
	ParentID    uint   `gorm:"default:0" json:"parentId" column_setting:"true"`
	FeRoute     string `gorm:"type:varchar(255)" json:"feRoute" column_setting:"true"`
	FeComponent string `gorm:"type:varchar(255)" json:"feComponent" column_setting:"true"`
	Sort        int    `gorm:"default:0" json:"sort" column_setting:"true"`
	Icon        string `gorm:"type:varchar(50)" json:"icon" column_setting:"true"`
	IsLink      bool   `gorm:"default:false" json:"isLink" column_setting:"true"`
	IsCache     bool   `gorm:"default:true" json:"isCache" column_setting:"true"`
	Visible     bool   `gorm:"default:true" json:"visible" column_setting:"true"`
	Status      bool   `gorm:"default:true" json:"status" column_setting:"true"`
	BeApis      string `gorm:"type:text" json:"beApis" column_setting:"true"`                           // JSON array: ["GET /api/v1/users"]
	Position    string `gorm:"type:varchar(20);default:'row'" json:"position" column_setting:"true"`    // header, row
	BtnType     string `gorm:"type:varchar(20);default:'primary'" json:"btnType" column_setting:"true"` // primary, error, warning
}
