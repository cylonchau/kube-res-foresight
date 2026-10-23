package router

import (
	"github.com/cylonchau/kubeforecast/pkg/app/middleware"
	"github.com/cylonchau/kubeforecast/pkg/config"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/workflow"
	"github.com/gin-gonic/gin"

	v1 "github.com/cylonchau/kubeforecast/pkg/app/server/api"
)

// RegisteredRouter composes the business-neutral platform APIs.
func RegisteredRouter(e *gin.Engine, workflowEngine *workflow.Engine) {
	e.GET("/ping", func(c *gin.Context) { c.JSON(200, gin.H{"message": "pong"}) })

	v1Group := e.Group("/api/v1")

	authHandler := &v1.AuthRouter{
		UserDAO: rdb.NewUserDAO(model.DB), PermDAO: rdb.NewPermissionDAO(model.DB),
		ConfigDAO: rdb.NewSysConfigDAO(model.DB), RefreshDAO: rdb.NewRefreshTokenSessionDAO(model.DB),
		SecurityDAO: rdb.NewLoginSecurityDAO(model.DB), LoginAuditDAO: rdb.NewLoginAuditLogDAO(model.DB),
	}
	authPublicGroup := v1Group.Group("/auth")
	authPublicGroup.GET("/login/captcha", authHandler.LoginCaptcha)
	authPublicGroup.POST("/login", authHandler.Login)
	authPublicGroup.POST("/login/otp", authHandler.LoginOTP)
	authPublicGroup.POST("/refresh", authHandler.RefreshToken)
	authPublicGroup.POST("/logout", authHandler.Logout)

	telegramHandler := &v1.TelegramRouter{
		UserDAO: rdb.NewUserDAO(model.DB), BindingDAO: rdb.NewTelegramBindingDAO(model.DB),
		RefreshDAO: rdb.NewRefreshTokenSessionDAO(model.DB), LoginAuditDAO: rdb.NewLoginAuditLogDAO(model.DB),
		ConfigDAO: rdb.NewSysConfigDAO(model.DB), TemplateDAO: rdb.NewNotifyTemplateDAO(model.DB),
		InstanceDAO: rdb.NewWorkflowInstanceDAO(model.DB), Engine: workflowEngine,
	}
	telegramPublicGroup := v1Group.Group("/telegram")
	telegramPublicGroup.POST("/auth/login", telegramHandler.Login)
	if config.CONFIG != nil && config.CONFIG.Server.DevMode {
		telegramPublicGroup.POST("/auth/mock-login", telegramHandler.MockLogin)
	}
	telegramPublicGroup.POST("/bind/confirm", telegramHandler.ConfirmBind)

	v1Group.Use(middleware.JWTAuthMiddleware(rdb.NewRefreshTokenSessionDAO(model.DB), rdb.NewSecretKeyDAO(model.DB)))
	v1Group.Use(middleware.CasbinMiddleware(rdb.NewPermissionDAO(model.DB)))
	v1Group.Use(middleware.OperationLogMiddleware(rdb.NewOperationLogDAO(model.DB)))
	// Protected groups must be created after the middleware above. Gin route
	// groups snapshot their handler chain at creation time, so reusing either
	// public group here would leave protected routes without JWT context.
	authGroup := v1Group.Group("/auth")
	telegramGroup := v1Group.Group("/telegram")

	authGroup.GET("/info", authHandler.GetInfo)
	authGroup.GET("/get-routers", authHandler.GetRouters)
	authGroup.GET("/session-status", authHandler.SessionStatus)
	authGroup.POST("/otp/generate", authHandler.GenerateOTP)
	authGroup.POST("/otp/bind", authHandler.BindOTP)
	authGroup.POST("/otp/unbind", authHandler.UnbindOTP)
	authGroup.PUT("/password", authHandler.UpdatePassword)
	authGroup.PUT("/profile", authHandler.UpdateProfile)
	authGroup.PUT("/language", authHandler.UpdateLanguage)

	operationLogHandler := &v1.OperationLogRouter{DAO: rdb.NewOperationLogDAO(model.DB)}
	v1Group.GET("/operation-logs", operationLogHandler.List)
	v1Group.DELETE("/operation-logs", operationLogHandler.Clear)

	telegramGroup.POST("/bind/generate", telegramHandler.GenerateBindToken)
	telegramGroup.POST("/unbind", telegramHandler.Unbind)
	telegramGroup.GET("/status", telegramHandler.GetStatus)
	telegramGroup.GET("/admin/bindings", telegramHandler.AdminListBindings)
	telegramGroup.POST("/admin/bindings/batch-delete", telegramHandler.AdminBatchUnbind)
	telegramGroup.DELETE("/admin/bindings/:id", telegramHandler.AdminUnbind)
	telegramGroup.GET("/admin/templates", telegramHandler.AdminListTemplates)
	telegramGroup.PUT("/admin/templates/:key", telegramHandler.AdminUpdateTemplate)

	workflowHandler := &v1.WorkflowRouter{
		CommandDAO: rdb.NewBotCommandDAO(model.DB), WorkflowDAO: rdb.NewWorkflowDefDAO(model.DB),
		InstanceDAO: rdb.NewWorkflowInstanceDAO(model.DB), Engine: workflowEngine, UserDAO: rdb.NewUserDAO(model.DB),
		BotDAO: rdb.NewTelegramBotDAO(model.DB),
	}
	workflowHandler.RegisterRoutes(telegramGroup)

	userHandler := &v1.UserRouter{DAO: rdb.NewUserDAO(model.DB), RefreshDAO: rdb.NewRefreshTokenSessionDAO(model.DB), LoginAuditDAO: rdb.NewLoginAuditLogDAO(model.DB)}
	userGroup := v1Group.Group("/users")
	userGroup.GET("", userHandler.List)
	userGroup.POST("", userHandler.Create)
	userGroup.POST("/batch-delete", userHandler.BatchDelete)
	userGroup.PUT("/:id", userHandler.Update)
	userGroup.DELETE("/:id", userHandler.Delete)
	userGroup.POST("/otp/reset/:id", userHandler.ResetOTP)

	roleHandler := &v1.RoleRouter{DAO: rdb.NewRoleDAO(model.DB)}
	roleGroup := v1Group.Group("/roles")
	roleGroup.GET("", roleHandler.List)
	roleGroup.GET("/all", roleHandler.ListAll)
	roleGroup.POST("", roleHandler.Create)
	roleGroup.POST("/batch-delete", roleHandler.BatchDelete)
	roleGroup.PUT("/:id", roleHandler.Update)
	roleGroup.DELETE("/:id", roleHandler.Delete)

	permissionHandler := &v1.PermissionRouter{DAO: rdb.NewPermissionDAO(model.DB)}
	permissionGroup := v1Group.Group("/permissions")
	permissionGroup.GET("", permissionHandler.ListTree)
	permissionGroup.POST("", permissionHandler.Create)
	permissionGroup.PUT("/:id", permissionHandler.Update)
	permissionGroup.DELETE("/:id", permissionHandler.Delete)

	configHandler := &v1.ConfigRouter{DAO: rdb.NewSysConfigDAO(model.DB)}
	configGroup := v1Group.Group("/configs")
	configGroup.GET("", configHandler.List)
	configGroup.GET("/:key", configHandler.Get)
	configGroup.PUT("/:key", configHandler.Update)

	secretHandler := &v1.SecretKeyRouter{DAO: rdb.NewSecretKeyDAO(model.DB), PermDAO: rdb.NewPermissionDAO(model.DB)}
	secretGroup := v1Group.Group("/secret-keys")
	secretGroup.GET("", secretHandler.List)
	secretGroup.POST("", secretHandler.Create)
	secretGroup.GET("/permission-options", secretHandler.PermissionOptions)
	secretGroup.GET("/:id", secretHandler.Get)
	secretGroup.PUT("/:id", secretHandler.Update)
	secretGroup.DELETE("/:id", secretHandler.Delete)

	columnHandler := &v1.ColumnConfigRouter{MetaDAO: &rdb.ColumnMetaDAO{}, ConfigDAO: rdb.NewUserColumnConfigDAO(model.DB), SysConfigDAO: rdb.NewSysConfigDAO(model.DB)}
	columnGroup := v1Group.Group("/columns")
	columnGroup.GET("/meta", columnHandler.GetMeta)
	columnGroup.GET("/config", columnHandler.GetConfig)
	columnGroup.POST("/config", columnHandler.UpdateConfig)

	routeHandler := &v1.RouteRouter{}
	routeGroup := v1Group.Group("/route")
	routeGroup.GET("/get-constant-routes", routeHandler.GetConstantRoutes)
	routeGroup.GET("/is-route-exist", routeHandler.IsRouteExist)
	routeGroup.GET("/get-user-routes", routeHandler.GetUserRoutes)

	taskHandler := &v1.TaskRouter{DAO: rdb.NewTaskDefinitionDAO(model.DB)}
	taskGroup := v1Group.Group("/tasks")
	taskGroup.GET("", taskHandler.List)
	taskGroup.PUT("/:id", taskHandler.Update)

	jobHandler := v1.NewJobRouter(rdb.NewJobDAO(model.DB))
	jobGroup := v1Group.Group("/jobs")
	jobGroup.GET("", jobHandler.List)
	jobGroup.GET("/types", jobHandler.Types)
	jobGroup.POST("/reset", jobHandler.Reset)
	jobGroup.POST("/batch-reset", jobHandler.BatchReset)

	statsHandler := &v1.StatsRouter{DB: model.DB}
	v1Group.GET("/stats/stream", statsHandler.StreamStats)
}
