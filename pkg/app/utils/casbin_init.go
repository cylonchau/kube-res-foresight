package utils

import (
	"encoding/json"
	"fmt"
	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
	gormadapter "github.com/casbin/gorm-adapter/v3"
	"github.com/cylonchau/kube-resource-fcst/pkg/logger"
	hmodel "github.com/cylonchau/kube-resource-fcst/pkg/model"
	"strings"
)

var Enforcer *casbin.Enforcer

// InitCasbin initializes Casbin from DB config
func InitCasbin() error {
	const modelStr = `
[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act

[role_definition]
g = _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = g(r.sub, p.sub) && keyMatch2(r.obj, p.obj) && r.act == p.act
`

	// 1. Create Casbin Model
	m, err := model.NewModelFromString(modelStr)
	if err != nil {
		return fmt.Errorf("failed to load casbin model: %v", err)
	}

	// 3. Create GORM Adapter
	adapter, err := gormadapter.NewAdapterByDB(hmodel.DB)
	if err != nil {
		return fmt.Errorf("failed to create casbin adapter: %v", err)
	}

	// 4. Create Enforcer
	Enforcer, err = casbin.NewEnforcer(m, adapter)
	if err != nil {
		return fmt.Errorf("failed to create casbin enforcer: %v", err)
	}

	// 5. Load policies into memory
	err = Enforcer.LoadPolicy()
	if err != nil {
		return fmt.Errorf("failed to load casbin policies: %v", err)
	}

	logger.Info("Casbin initialized successfully from Database model config")
	return nil
}

// SyncPolicies loads all roles-permissions from DB and flushes into Casbin Rule table
func SyncPolicies() error {
	if Enforcer == nil {
		return fmt.Errorf("casbin enforcer is nil")
	}

	// Clear existing policies to rebuild fresh map
	Enforcer.ClearPolicy()

	db := hmodel.DB

	// 1. Fetch all Roles with Permissions
	var roles []hmodel.Role
	if err := db.Preload("Permissions").Find(&roles).Error; err != nil {
		return err
	}

	for _, r := range roles {
		for _, p := range r.Permissions {
			if p.BeApis == "" {
				continue
			}
			var apiList []string
			if err := json.Unmarshal([]byte(p.BeApis), &apiList); err != nil {
				continue // Skip malformed JSON
			}

			for _, api := range apiList {
				// API example: "GET /api/v1/users"
				parts := strings.Fields(api)
				if len(parts) >= 2 {
					method := strings.ToUpper(parts[0])
					path := parts[1]
					Enforcer.AddPolicy(r.Code, path, method)
				}
			}
		}
	}

	// 2. Sync user-role grouping policies
	var users []hmodel.User
	if err := db.Preload("Roles").Find(&users).Error; err == nil {
		for _, u := range users {
			for _, r := range u.Roles {
				Enforcer.AddGroupingPolicy(u.Username, r.Code)
			}
		}
	}

	// Save back to DB table casbin_rule
	return Enforcer.SavePolicy()
}
