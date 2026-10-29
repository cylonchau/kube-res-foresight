package migration

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/cylonchau/kube-resource-fcst/pkg/ldapauth"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// menuSuccessors maps a retired menu code to the codes that replaced it. A role that could open the old page
// keeps access to everything the page was split into.
var menuSuccessors = map[string][]string{
	"forecast_history":             {"forecast-results_history"},
	"forecast_evaluation":          {"forecast-results_evaluations", "forecast-results_leaderboard"},
	"forecast_recommendations":     {"forecast-actions_recommendations", "forecast-actions_executions", "forecast-actions_fallbacks"},
	"forecast_metrics":             {"monitoring_datasets", "monitoring_curves"},
	"monitoring_metrics":           {"monitoring_curves", "monitoring_datasets"},
	"forecast-results_backtests":   {"forecast-results_evaluations"},
	"forecast_modules":             {"forecast-modules_modules", "monitoring_datasets"},
	"forecast-modules_bindings":    {"forecast-modules_modules"},
	"forecast_catalog":             {"forecast_algorithms"},
	"forecast_sources":             {"monitoring_datasets"},
	"forecast-modules_sources":     {"monitoring_datasets"},
	"forecast_scores":              {"forecast_score-records", "forecast_ranking"},
	"forecast_ranking-history":     {"forecast_ranking"},
	"forecast-results_trends":      {"forecast-results_history"},
	"forecast_algorithm-templates": {"forecast_algorithms"},
	"forecast_input-sets":          {"forecast_algorithms"},
	"forecast-results_runs":        {"forecast-results_algorithm-runs"},
	"forecast-actions_snapshots":   {"forecast-actions_recommendations"},
	"datasource_presets":           {"datasource_metric-mappings", "forecast-modules_modules"},
}

// migrateMenuGrants copies the grants of retired menus to their successors (and to the successors' parent
// directories) before the retired rows are removed, so an upgrade never takes a page away from a role. It is a
// no-op once the retired rows are gone.
func migrateMenuGrants(db *gorm.DB) error {
	for oldCode, newCodes := range menuSuccessors {
		var old model.Permission
		if err := db.Where("code = ?", oldCode).First(&old).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				continue
			}
			return err
		}
		var roleIDs []uint
		if err := db.Table("sys_role_permissions").Where("permission_id = ?", old.ID).Pluck("role_id", &roleIDs).Error; err != nil {
			return err
		}
		if len(roleIDs) == 0 {
			continue
		}
		for _, code := range newCodes {
			var next model.Permission
			if err := db.Where("code = ?", code).First(&next).Error; err != nil {
				if err == gorm.ErrRecordNotFound {
					continue // not seeded (yet): the seed grants the administrator everything
				}
				return err
			}
			ids := []uint{next.ID}
			if next.ParentID != 0 {
				ids = append(ids, next.ParentID)
			}
			for _, roleID := range roleIDs {
				for _, id := range ids {
					var n int64
					if err := db.Table("sys_role_permissions").Where("role_id = ? AND permission_id = ?", roleID, id).Count(&n).Error; err != nil {
						return err
					}
					if n == 0 {
						if err := db.Exec("INSERT INTO sys_role_permissions (role_id, permission_id) VALUES (?, ?)", roleID, id).Error; err != nil {
							return err
						}
					}
				}
			}
		}
	}
	return nil
}

// legacyLDAPTables are the tables the LDAP configuration used before it moved into the system parameters.
var legacyLDAPTables = []string{"ldap_setting", "ldap_group_mapping"}

type legacyLDAPSetting struct {
	Enabled         bool
	URL             string
	StartTLS        bool `gorm:"column:start_tls"`
	SkipVerify      bool
	CACert          string `gorm:"column:ca_cert"`
	BindDN          string `gorm:"column:bind_dn"`
	BindPassword    string
	UserBaseDN      string `gorm:"column:user_base_dn"`
	UserFilter      string
	DisplayNameAttr string
	EmailAttr       string
	PhoneAttr       string
	GroupBaseDN     string `gorm:"column:group_base_dn"`
	GroupFilter     string
	GroupNameAttr   string
	TimeoutSeconds  int
	DefaultRoleCode string
}

// migrateLDAPToSystemParameters copies the saved directory configuration and group mappings of an older
// installation into the ldap.* system parameters. It never overwrites parameters that already exist, so it is safe
// to run again. The legacy tables are dropped afterwards by the retired-table cleanup, which prints them first.
func migrateLDAPToSystemParameters(db *gorm.DB) error {
	if !db.Migrator().HasTable("ldap_setting") {
		return nil
	}
	var existing int64
	if err := db.Model(&model.SysConfig{}).Where("config_key LIKE ?", ldapauth.KeyPrefix+"%").Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}
	var legacy legacyLDAPSetting
	res := db.Table("ldap_setting").Where("id = 1").Limit(1).Find(&legacy)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return nil
	}
	row := &model.LDAPSetting{
		Enabled: legacy.Enabled, URL: legacy.URL, StartTLS: legacy.StartTLS, SkipVerify: legacy.SkipVerify, CACert: legacy.CACert,
		BindDN: legacy.BindDN, BindPassword: legacy.BindPassword, UserBaseDN: legacy.UserBaseDN, UserFilter: legacy.UserFilter,
		DisplayNameAttr: legacy.DisplayNameAttr, EmailAttr: legacy.EmailAttr, PhoneAttr: legacy.PhoneAttr,
		GroupBaseDN: legacy.GroupBaseDN, GroupFilter: legacy.GroupFilter, GroupNameAttr: legacy.GroupNameAttr,
		TimeoutSeconds: legacy.TimeoutSeconds, DefaultRoleCode: legacy.DefaultRoleCode,
	}
	if db.Migrator().HasTable("ldap_group_mapping") {
		var mappings []struct {
			GroupName string
			RoleCode  string
		}
		if err := db.Table("ldap_group_mapping").Order("group_name ASC, role_code ASC").Find(&mappings).Error; err != nil {
			return err
		}
		lines := make([]string, 0, len(mappings))
		for _, m := range mappings {
			lines = append(lines, strings.TrimSpace(m.GroupName)+"="+strings.TrimSpace(m.RoleCode))
		}
		row.GroupRoleMappings = strings.Join(lines, "\n")
	}
	if err := (&ldapauth.Service{DB: db}).Save(context.Background(), row); err != nil {
		return fmt.Errorf("move the LDAP configuration into the system parameters: %w", err)
	}
	fmt.Printf("upgrade: moved the LDAP configuration (%s, enabled=%v) into the ldap.* system parameters\n", row.URL, row.Enabled)
	return nil
}
