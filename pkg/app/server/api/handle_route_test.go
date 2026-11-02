package api

import (
	"encoding/json"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnsureBuiltInSelfServiceRoutesAddsHiddenUserCenter(t *testing.T) {
	routes := ensureBuiltInSelfServiceRoutes([]model.Permission{{Code: "home"}})
	if len(routes) != 2 {
		t.Fatalf("got %d routes, want 2", len(routes))
	}
	userCenter := routes[1]
	if userCenter.Code != "user-center" || userCenter.FeRoute != "/user-center" || userCenter.Visible {
		t.Fatalf("unexpected user-center route: %#v", userCenter)
	}

	routes = ensureBuiltInSelfServiceRoutes(routes)
	if len(routes) != 2 {
		t.Fatalf("built-in route was duplicated; got %d routes", len(routes))
	}
}

func TestFilterRetiredRoutePermissions(t *testing.T) {
	routes := filterRetiredRoutePermissions([]model.Permission{
		{Code: "telegram"},
		{Code: "telegram_workflows"},
		{Code: "node_bot-api-nodes"},
		{Code: "workflow_pipelines"},
		{Code: "forecast-modules_modules"},
	})
	if len(routes) != 1 || routes[0].Code != "forecast-modules_modules" {
		t.Fatalf("unexpected filtered routes: %#v", routes)
	}
}

func TestIsRouteExistRejectsRetiredRoutesWithoutMigration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, code := range []string{"telegram_workflows", "node_bot-api-nodes"} {
		response := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(response)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/route/is-route-exist?routeName="+code, nil)
		(&RouteRouter{}).IsRouteExist(ctx)

		var body struct {
			Data bool `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Data {
			t.Fatalf("retired route %q was reported as available", code)
		}
	}
}

func TestGetUserRoutesIncludesUserCenterBeforeMigration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Permission{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Permission{Name: "Home", Code: "home", Type: "MENU", FeRoute: "/home", FeComponent: "view.home", Visible: true, Status: true}).Error; err != nil {
		t.Fatal(err)
	}
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })

	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/route/get-user-routes", nil)
	ctx.Set("roles", []string{"ADMIN"})
	(&RouteRouter{}).GetUserRoutes(ctx)

	var body struct {
		Data struct {
			Routes []struct {
				Name string `json:"name"`
				Meta struct {
					HideInMenu bool `json:"hideInMenu"`
				} `json:"meta"`
			} `json:"routes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, route := range body.Data.Routes {
		if route.Name == "user-center" {
			found = true
			if !route.Meta.HideInMenu {
				t.Fatal("user-center compatibility route must be hidden from the sidebar")
			}
		}
	}
	if !found {
		t.Fatalf("user-center route missing from response: %s", response.Body.String())
	}
}

// Whoever may see the dashboard lands on it after login; the others land where they did before.
func TestTheDashboardIsTheLandingPageOfWhoeverMaySeeIt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	landing := func(withDashboard bool) string {
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.AutoMigrate(&model.Permission{}); err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.Permission{Name: "Modules", Code: "forecast-modules", Type: "DIRECTORY", FeRoute: "/forecast-modules", FeComponent: "layout.base", Visible: true, Status: true}).Error; err != nil {
			t.Fatal(err)
		}
		if withDashboard {
			if err := db.Create(&model.Permission{Name: "Dashboard", Code: "dashboard", Type: "MENU", FeRoute: "/dashboard", FeComponent: "view.dashboard", Visible: true, Status: true}).Error; err != nil {
				t.Fatal(err)
			}
		}
		previousDB := model.DB
		model.DB = db
		t.Cleanup(func() { model.DB = previousDB })
		response := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(response)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/route/get-user-routes", nil)
		ctx.Set("roles", []string{"ADMIN"})
		(&RouteRouter{}).GetUserRoutes(ctx)
		var body struct {
			Data struct {
				Home string `json:"home"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Data.Home
	}
	if got := landing(true); got != "dashboard" {
		t.Fatalf("landing page with the dashboard: %q", got)
	}
	if got := landing(false); got != homeRoute {
		t.Fatalf("landing page without it: %q", got)
	}
}
