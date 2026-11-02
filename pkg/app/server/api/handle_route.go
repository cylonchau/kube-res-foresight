package api

import (
	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/gin-gonic/gin"
	"sort"
	"strings"
)

type RouteRouter struct{}

// retiredRouteCodes are menu codes of removed features; a database that still holds them never routes to them.
var retiredRouteCodes = map[string]struct{}{
	"node_bot-api-nodes": {},
	"telegram_workflows": {},
	"workflow":           {},
	"workflow_pipelines": {},
	"workflow_instances": {},
}

func (rr *RouteRouter) GetConstantRoutes(c *gin.Context) {
	c.JSON(200, gin.H{"code": 40404, "msg": "Use local constant routes"})
}

func (rr *RouteRouter) IsRouteExist(c *gin.Context) {
	routeName := c.Query("routeName")
	if !model.IsPlatformRoute(routeName) {
		query.SuccessResponse(c, nil, false)
		return
	}
	if _, retired := retiredRouteCodes[routeName]; retired {
		query.SuccessResponse(c, nil, false)
		return
	}
	if routeName == "user-center" {
		query.SuccessResponse(c, nil, true)
		return
	}
	var count int64
	if err := model.DB.Model(&model.Permission{}).Where("code = ?", routeName).Count(&count).Error; err != nil {
		query.SuccessResponse(c, nil, false)
		return
	}
	query.SuccessResponse(c, nil, count > 0)
}

func (rr *RouteRouter) GetUserRoutes(c *gin.Context) {
	permissions, err := routePermissionsForCurrentUser(c)
	if err != nil {
		query.SuccessResponse(c, nil, gin.H{"home": homeRoute, "routes": []gin.H{}})
		return
	}
	permissions = filterRetiredRoutePermissions(ensureBuiltInSelfServiceRoutes(permissions))
	buttons := make(map[uint][]gin.H)
	menus := make([]model.Permission, 0, len(permissions))
	for _, permission := range permissions {
		if permission.Type == "BUTTON" {
			buttons[permission.ParentID] = append(buttons[permission.ParentID], gin.H{"code": permission.Code, "label": permission.Name, "icon": permission.Icon, "position": permission.Position, "btnType": permission.BtnType})
		} else {
			menus = append(menus, permission)
		}
	}
	children := make(map[uint][]gin.H)
	for _, permission := range menus {
		if permission.ParentID == 0 {
			continue
		}
		children[permission.ParentID] = append(children[permission.ParentID], routeItem(permission, buttons[permission.ID]))
	}
	roots := make([]gin.H, 0)
	for _, permission := range menus {
		if permission.ParentID != 0 || permission.FeRoute == "" {
			continue
		}
		item := routeItem(permission, buttons[permission.ID])
		if nested := children[permission.ID]; len(nested) > 0 {
			item["children"] = nested
		} else if permission.Code == "home" {
			item["component"] = "layout.base$view.home"
		} else if component, ok := item["component"].(string); ok && strings.HasPrefix(component, "view.") {
			item["component"] = "layout.base$" + component
		}
		roots = append(roots, item)
	}
	home := homeRoute
	for _, root := range roots {
		if root["name"] == "dashboard" {
			home = "dashboard" // whoever may see the home page lands on it
		}
	}
	query.SuccessResponse(c, nil, gin.H{"home": home, "routes": roots})
}

func filterRetiredRoutePermissions(permissions []model.Permission) []model.Permission {
	filtered := make([]model.Permission, 0, len(permissions))
	for _, permission := range permissions {
		if permission.Type != "BUTTON" && !model.IsPlatformRoute(permission.Code) {
			continue
		}
		if _, retired := retiredRouteCodes[permission.Code]; retired {
			continue
		}
		filtered = append(filtered, permission)
	}
	return filtered
}

func routePermissionsForCurrentUser(c *gin.Context) ([]model.Permission, error) {
	if roles, ok := c.Get("roles"); ok {
		if list, ok := roles.([]string); ok {
			for _, role := range list {
				if role == "ADMIN" {
					var all []model.Permission
					err := model.DB.Where("status = ?", true).Order("sort asc").Find(&all).Error
					return all, err
				}
			}
		}
	}
	userID, _ := c.Get("userId")
	var user model.User
	if err := model.DB.Preload("Roles.Permissions").First(&user, userID).Error; err != nil {
		return nil, err
	}
	byID := make(map[uint]model.Permission)
	for _, role := range user.Roles {
		if !role.Status {
			continue
		}
		for _, permission := range role.Permissions {
			if permission.Status {
				byID[permission.ID] = permission
			}
		}
	}
	var all []model.Permission
	if err := model.DB.Where("status = ?", true).Find(&all).Error; err != nil {
		return nil, err
	}
	lookup := make(map[uint]model.Permission, len(all))
	for _, permission := range all {
		lookup[permission.ID] = permission
		if permission.Code == "home" || permission.Code == "user-center" {
			byID[permission.ID] = permission
		}
	}
	for _, permission := range byID {
		for parentID := permission.ParentID; parentID > 0; {
			parent, ok := lookup[parentID]
			if !ok {
				break
			}
			byID[parent.ID] = parent
			parentID = parent.ParentID
		}
	}
	result := make([]model.Permission, 0, len(byID))
	for _, permission := range byID {
		result = append(result, permission)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Sort < result[j].Sort })
	return result, nil
}

func ensureBuiltInSelfServiceRoutes(permissions []model.Permission) []model.Permission {
	for _, permission := range permissions {
		if permission.Code == "user-center" {
			return permissions
		}
	}
	return append(permissions, model.Permission{
		ID:          ^uint(0),
		Name:        "User Center",
		Code:        "user-center",
		Type:        "MENU",
		FeRoute:     "/user-center",
		FeComponent: "view.user-center",
		Sort:        1,
		Visible:     false,
		Status:      true,
	})
}

func routeItem(permission model.Permission, buttons []gin.H) gin.H {
	component := permission.FeComponent
	if component == "base" {
		component = "layout.base"
	}
	if component == "view" || component == "" {
		component = "view." + permission.Code
	}
	return gin.H{
		"name": permission.Code, "path": permission.FeRoute, "component": component,
		"meta": gin.H{"title": permission.Name, "i18nKey": "route." + permission.Code, "icon": permission.Icon, "order": permission.Sort, "hideInMenu": !permission.Visible, "buttons": buttons},
	}
}

// homeRoute is the landing page after login.
const homeRoute = "forecast-modules_modules"
