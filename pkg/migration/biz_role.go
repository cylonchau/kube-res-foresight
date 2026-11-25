package migration

import (
	"encoding/json"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"gorm.io/gorm"
)

// The API routes a menu needs, and the business role.
//
// A role reaches an API only through a permission that lists it ("METHOD /path"). The pattern language is the one of the permission check
// (Casbin keyMatch2): ":id" stands for one path segment, "/*" for any path below, and a star that does not follow a slash means nothing, so
// "/api/v1/modules*" would match "/api/v1/modules" and not "/api/v1/modules/5". Every list here therefore names a collection as two entries,
// "/api/v1/x" and "/api/v1/x/*".

// both is a collection and everything below it, for the methods given.
func both(path string, methods ...string) []string {
	out := make([]string, 0, len(methods)*2)
	for _, m := range methods {
		out = append(out, m+" "+path, m+" "+path+"/*")
	}
	return out
}

func apiList(groups ...[]string) string {
	list := []string{}
	for _, g := range groups {
		list = append(list, g...)
	}
	raw, _ := json.Marshal(list)
	return string(raw)
}

func get(paths ...string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, "GET "+p)
	}
	return out
}

var (
	readMethods  = []string{"GET"}
	writeMethods = []string{"GET", "POST", "PUT", "DELETE"}
)

// chatAPIs is what the chat needs: asking (at once or in the background), the commands, the file of a status report, the user's own
// conversations (listed, read, renamed, deleted) and the background tasks of the questions. Every one of them is bound to the signed-in user in
// the handler, so granting them gives a user nothing of anybody else's.
func chatAPIs() []string {
	return apiList2(
		get("/api/v1/assistant/status", "/api/v1/assistant/export"),
		[]string{"POST /api/v1/assistant/chat", "POST /api/v1/assistant/skill", "POST /api/v1/assistant/chat/async", "POST /api/v1/assistant/mail-me"},
		both("/api/v1/assistant/conversations", writeMethods...),
		both("/api/v1/assistant/async", readMethods...),
		both("/api/v1/assistant/async", "POST"),
	)
}

func apiList2(groups ...[]string) []string {
	var out []string
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

// moduleReads are the reads every forecast page makes about modules.
func moduleReads() []string { return both("/api/v1/modules", readMethods...) }

// menuAPIs are the APIs of each menu of the forecasting pages and of the chat: a role that holds the menu can use the page.
var menuAPIs = map[string][]string{
	"forecast-modules_modules": apiList2(
		both("/api/v1/modules", writeMethods...),
		get("/api/v1/datasources/options", "/api/v1/metrics/catalog", "/api/v1/dataset/catalog", "/api/v1/metric-presets"),
		both("/api/v1/metric-mappings", readMethods...),
		both("/api/v1/forecast-metrics", readMethods...),
		both("/api/v1/algorithms", readMethods...),
		[]string{"POST /api/v1/metric-queries/test", "POST /api/v1/controls/*"},
	),
	"forecast-results_history": apiList2(moduleReads(), get("/api/v1/predictions"), both("/api/v1/forecast-metrics", readMethods...), both("/api/v1/algorithms", readMethods...)),
	"forecast-results_effect":  apiList2(moduleReads(), get("/api/v1/dataset/catalog")),
	"forecast-results_evaluations": apiList2(moduleReads(), get("/api/v1/evaluations", "/api/v1/backtests"),
		both("/api/v1/algorithms", readMethods...)),
	"forecast-results_leaderboard": apiList2(moduleReads(), both("/api/v1/algorithm-scores", readMethods...), both("/api/v1/algorithm-rankings", readMethods...),
		both("/api/v1/algorithms", readMethods...)),
	"forecast-results_overprovision": apiList2(moduleReads(), get("/api/v1/overprovision/resources", "/api/v1/overprovision/ai-advice", "/api/v1/grafana/settings", "/api/v1/grafana/dashboard")),
	// the page of one module (the second level of the resource dashboard) and the cluster dashboard (its own page, with its own clusters)
	"forecast-results_overprovision-detail": apiList2(moduleReads(), get("/api/v1/grafana/settings", "/api/v1/grafana/dashboard")),
	"forecast-results_cluster-dashboard": apiList2(get("/api/v1/overprovision/cluster", "/api/v1/overprovision/clusters", "/api/v1/grafana/settings", "/api/v1/grafana/dashboard"),
		both("/api/v1/inventory", readMethods...)),
	"forecast-results_algorithm-runs":  apiList2(moduleReads(), both("/api/v1/training-runs", readMethods...), both("/api/v1/algorithms", readMethods...)),
	"forecast-actions_recommendations": apiList2(moduleReads(), get("/api/v1/recommendations", "/api/v1/resource-analysis"), both("/api/v1/snapshots", readMethods...)),
	"forecast-actions_executions":      apiList2(moduleReads(), get("/api/v1/scaling-decisions", "/api/v1/scale-outcomes")),
	"forecast-actions_fallbacks":       apiList2(moduleReads(), get("/api/v1/fallbacks"), both("/api/v1/snapshots", readMethods...)),
	"forecast-actions_admissions":      apiList2(moduleReads(), get("/api/v1/admissions")),
	"ai-config_chat":                   chatAPIs(),
	"assistant:chat":                   chatAPIs(),
}

// applyMenuAPIs writes the APIs into the menus they belong to (the seed rows carry the menus, this carries what each one needs). It runs after
// the seed, so an upgraded database gets them too; a menu that does not exist is skipped.
func applyMenuAPIs(db *gorm.DB) error {
	for code, list := range menuAPIs {
		raw, err := json.Marshal(list)
		if err != nil {
			return err
		}
		if err := db.Model(&model.Permission{}).Where("code = ?", code).UpdateColumn("be_apis", string(raw)).Error; err != nil {
			return err
		}
	}
	return nil
}

// BusinessRoleCode is the code of the role for the people who use the forecasts and do not administer the platform.
const BusinessRoleCode = "BIZ"

// businessPermissions are the menus and the button of the business role: module management, forecast results, recommendations and execution, and
// the chat of the AI assistant (the directory it sits in, the page, and the button that lets it ask). Nothing of the system, the authorization, the
// data sources, the algorithms, the task center, the AI configuration (models, skills, tasks, audit) or the diagnostics.
var businessPermissions = []string{
	"forecast-modules", "forecast-modules_modules",
	"forecast-results", "forecast-results_history", "forecast-results_effect", "forecast-results_evaluations", "forecast-results_leaderboard", "forecast-results_overprovision", "forecast-results_overprovision-detail", "forecast-results_cluster-dashboard", "forecast-results_algorithm-runs",
	"forecast-actions", "forecast-actions_recommendations", "forecast-actions_executions", "forecast-actions_fallbacks", "forecast-actions_admissions",
	"ai-config", "ai-config_chat", "assistant:chat",
}

// seedBusinessRole makes the business role the first time, with the permissions above. A role that exists is left as the administrator made it: it
// is theirs to change, and a permission they took away is not given back by the next upgrade.
func seedBusinessRole(db *gorm.DB) error {
	var existing int64
	if err := db.Model(&model.Role{}).Where("code = ?", BusinessRoleCode).Count(&existing).Error; err != nil || existing > 0 {
		return err
	}
	var permissions []model.Permission
	if err := db.Where("code IN ?", businessPermissions).Find(&permissions).Error; err != nil {
		return err
	}
	role := model.Role{Name: "Business", Code: BusinessRoleCode, Status: true,
		Remark:      "Module management, forecast results, recommendations and execution, and the chat of the AI assistant. Nothing of the system or the AI configuration.",
		Permissions: permissions}
	return db.Create(&role).Error
}
