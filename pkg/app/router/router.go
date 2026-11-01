package router

import (
	"github.com/cylonchau/kube-resource-fcst/pkg/app/middleware"
	"github.com/cylonchau/kube-resource-fcst/pkg/assistant"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecastsvc"
	"github.com/cylonchau/kube-resource-fcst/pkg/ldapauth"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/gin-gonic/gin"

	v1 "github.com/cylonchau/kube-resource-fcst/pkg/app/server/api"
)

// RegisteredRouter composes the business-neutral platform APIs.
func RegisteredRouter(e *gin.Engine) {
	e.GET("/ping", func(c *gin.Context) { c.JSON(200, gin.H{"message": "pong"}) })

	v1Group := e.Group("/api/v1")
	// a body that is not UTF-8 would be stored as unrecoverable garbage: refused before any handler reads it
	v1Group.Use(middleware.UTF8BodyMiddleware())

	ldapService := &ldapauth.Service{DB: model.DB, Dir: ldapauth.Client{}, RevokeSessions: rdb.NewRefreshTokenSessionDAO(model.DB).RevokeUser}
	authHandler := &v1.AuthRouter{
		UserDAO: rdb.NewUserDAO(model.DB), PermDAO: rdb.NewPermissionDAO(model.DB),
		ConfigDAO: rdb.NewSysConfigDAO(model.DB), RefreshDAO: rdb.NewRefreshTokenSessionDAO(model.DB),
		SecurityDAO: rdb.NewLoginSecurityDAO(model.DB), LoginAuditDAO: rdb.NewLoginAuditLogDAO(model.DB),
		LDAP: ldapService,
	}
	authPublicGroup := v1Group.Group("/auth")
	authPublicGroup.GET("/login/captcha", authHandler.LoginCaptcha)
	authPublicGroup.POST("/login", authHandler.Login)
	authPublicGroup.POST("/login/otp", authHandler.LoginOTP)
	authPublicGroup.POST("/refresh", authHandler.RefreshToken)
	authPublicGroup.POST("/logout", authHandler.Logout)

	v1Group.Use(middleware.JWTAuthMiddleware(rdb.NewRefreshTokenSessionDAO(model.DB), rdb.NewSecretKeyDAO(model.DB)))
	v1Group.Use(middleware.CasbinMiddleware(rdb.NewPermissionDAO(model.DB)))
	v1Group.Use(middleware.OperationLogMiddleware(rdb.NewOperationLogDAO(model.DB)))
	// Protected groups must be created after the middleware above. Gin route
	// groups snapshot their handler chain at creation time, so reusing either
	// public group here would leave protected routes without JWT context.
	authGroup := v1Group.Group("/auth")

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
	loginLogHandler := &v1.LoginLogRouter{DAO: rdb.NewLoginAuditLogDAO(model.DB)}
	v1Group.GET("/login-logs", loginLogHandler.List)

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

	datasourceHandler := &v1.DatasourceRouter{DAO: rdb.NewDatasourceDAO(model.DB)}
	datasourceGroup := v1Group.Group("/datasources")
	datasourceGroup.GET("", datasourceHandler.List)
	datasourceGroup.GET("/options", datasourceHandler.Options)
	datasourceGroup.POST("", datasourceHandler.Create)
	datasourceGroup.POST("/batch-delete", datasourceHandler.BatchDelete)
	datasourceGroup.GET("/:id", datasourceHandler.Get)
	datasourceGroup.PUT("/:id", datasourceHandler.Update)
	datasourceGroup.DELETE("/:id", datasourceHandler.Delete)
	datasourceGroup.POST("/:id/test", datasourceHandler.Test)

	metricMappingHandler := &v1.MetricMappingRouter{DAO: rdb.NewMetricMappingDAO(model.DB)}
	v1Group.GET("/metrics/catalog", metricMappingHandler.Catalog)
	mappingGroup := v1Group.Group("/metric-mappings")
	mappingGroup.GET("", metricMappingHandler.List)
	mappingGroup.POST("", metricMappingHandler.Create)
	mappingGroup.POST("/batch-delete", metricMappingHandler.BatchDelete)
	mappingGroup.GET("/:id", metricMappingHandler.Get)
	mappingGroup.PUT("/:id", metricMappingHandler.Update)
	mappingGroup.DELETE("/:id", metricMappingHandler.Delete)

	forecastService := forecastsvc.New(rdb.NewModuleDAO(model.DB), rdb.NewMetricMappingDAO(model.DB), rdb.NewDatasourceDAO(model.DB),
		rdb.NewSampleDAO(model.DB), rdb.NewPredictionDAO(model.DB))
	forecastHandler := &v1.ForecastRouter{Modules: rdb.NewModuleDAO(model.DB), Configs: rdb.NewAlgorithmDAO(model.DB), Mappings: rdb.NewMetricMappingDAO(model.DB),
		Predictions: rdb.NewPredictionDAO(model.DB), Service: forecastService, Config: rdb.NewSysConfigDAO(model.DB)}
	centerHandler := &v1.AlgorithmCenterRouter{Modules: rdb.NewModuleDAO(model.DB), Algorithms: rdb.NewAlgorithmDAO(model.DB), Predictions: rdb.NewPredictionDAO(model.DB),
		Tasks: rdb.NewTaskDefinitionDAO(model.DB), Jobs: rdb.NewJobDAO(model.DB), RunStates: rdb.NewRunStateDAO(model.DB), Service: forecastService}
	presetHandler := &v1.PresetRouter{Datasources: rdb.NewDatasourceDAO(model.DB), Mappings: rdb.NewMetricMappingDAO(model.DB)}
	v1Group.GET("/metric-presets", presetHandler.List)
	v1Group.POST("/metric-presets/:id/apply", presetHandler.Apply)
	v1Group.GET("/algorithms", forecastHandler.Algorithms)
	v1Group.GET("/algorithms/:id", forecastHandler.GetAlgorithm)
	v1Group.POST("/algorithms", forecastHandler.CreateAlgorithm)
	v1Group.PUT("/algorithms/:id", forecastHandler.UpdateAlgorithm)
	v1Group.DELETE("/algorithms/:id", forecastHandler.DeleteAlgorithm)
	v1Group.POST("/algorithms/batch", forecastHandler.BatchAlgorithms)
	v1Group.POST("/algorithm-formulas/check", forecastHandler.FormulaCheck)
	v1Group.GET("/algorithm-formulas/functions", forecastHandler.FormulaFunctions)
	v1Group.GET("/algorithms/:id/schema", forecastHandler.AlgorithmSchema)
	v1Group.GET("/algorithms/:id/usage", centerHandler.Usage)
	v1Group.GET("/dataset/catalog", forecastHandler.DatasetCatalog)
	v1Group.GET("/algorithm-scores", centerHandler.ListScores)
	v1Group.GET("/algorithm-scores/latest", centerHandler.LatestScores)
	v1Group.GET("/algorithm-rankings/current", centerHandler.CurrentRankings)
	v1Group.GET("/algorithm-rankings/history", centerHandler.HistoryRankings)
	v1Group.GET("/admissions", centerHandler.ListAdmissions)
	// modules: Kubernetes workloads; every enabled algorithm applies to each of them
	moduleGroup := v1Group.Group("/modules")
	moduleGroup.GET("", forecastHandler.ListModules)
	moduleGroup.POST("", forecastHandler.CreateModule)
	moduleGroup.POST("/batch", forecastHandler.BatchModules)
	moduleGroup.GET("/:id", forecastHandler.GetModule)
	moduleGroup.PUT("/:id", forecastHandler.UpdateModule)
	moduleGroup.DELETE("/:id", forecastHandler.DeleteModule)
	moduleGroup.GET("/:id/sources", forecastHandler.ListSources)
	moduleGroup.GET("/:id/requirements", forecastHandler.ModuleRequirements)
	moduleGroup.GET("/:id/metric-scores", centerHandler.MetricScores)
	moduleGroup.POST("/:id/sources", forecastHandler.CreateSource)
	moduleGroup.PUT("/:id/sources/:sid", forecastHandler.UpdateSource)
	moduleGroup.DELETE("/:id/sources/:sid", forecastHandler.DeleteSource)
	moduleGroup.GET("/:id/history", forecastHandler.ModuleHistory)
	moduleGroup.GET("/:id/compare", forecastHandler.ModuleCompare)
	moduleGroup.POST("/:id/collect", forecastHandler.ModuleCollect)
	moduleGroup.POST("/:id/predict", forecastHandler.ModulePredict)
	moduleGroup.GET("/:id/delay-report", forecastHandler.ModuleDelayReport)
	moduleGroup.GET("/:id/effect-report", forecastHandler.ModuleEffectReport)
	moduleGroup.POST("/:id/change-preview", forecastHandler.ModuleChangePreview)
	moduleGroup.POST("/:id/run", forecastHandler.ModuleRun)
	moduleGroup.POST("/:id/backtest", forecastHandler.ModuleBacktest)
	moduleGroup.GET("/:id/backtests", forecastHandler.ModuleBacktests)
	moduleGroup.POST("/:id/evaluate", forecastHandler.ModuleEvaluate)
	moduleGroup.GET("/:id/trace", centerHandler.Trace)
	moduleGroup.GET("/:id/overrides", forecastHandler.ListPeriods)
	moduleGroup.POST("/:id/overrides", forecastHandler.CreatePeriod)
	moduleGroup.PUT("/:id/overrides/:pid", forecastHandler.UpdatePeriod)
	moduleGroup.DELETE("/:id/overrides/:pid", forecastHandler.DeletePeriod)
	moduleGroup.POST("/:id/recommend", forecastHandler.Recommend)
	moduleGroup.POST("/:id/score", centerHandler.RunScore)
	moduleGroup.GET("/:id/ranking", centerHandler.Ranking)
	moduleGroup.GET("/:id/leaderboard", forecastHandler.Leaderboard)
	v1Group.GET("/forecast-metrics/catalog", forecastHandler.MetricCatalog)
	v1Group.GET("/forecast-metrics/query", forecastHandler.QueryMetrics)
	v1Group.POST("/metric-queries/test", forecastHandler.TestMetricQuery)
	v1Group.POST("/records/batch-delete", forecastHandler.BatchDeleteRecords)
	v1Group.GET("/evaluations", forecastHandler.ListEvaluations)
	v1Group.GET("/recommendations", forecastHandler.ListRecommendations)
	v1Group.GET("/snapshots", forecastHandler.ListSnapshots)
	v1Group.GET("/snapshots/:id", forecastHandler.GetSnapshot)
	v1Group.GET("/backtests", forecastHandler.SearchBacktests)
	v1Group.GET("/fallbacks", forecastHandler.ListFallbacks)
	v1Group.GET("/predictions", forecastHandler.ListPredictions)
	v1Group.GET("/training-runs", forecastHandler.ListTrainings)
	v1Group.GET("/training-runs/summary", forecastHandler.TrainingSummary)
	v1Group.GET("/training-runs/:id", forecastHandler.GetTraining)
	v1Group.GET("/training-runs/:id/data", forecastHandler.TrainingData)
	v1Group.POST("/controls/:id/bypass", forecastHandler.Bypass)
	v1Group.POST("/controls/:id/resume", forecastHandler.Resume)
	runtimeHandler := &v1.RuntimeRouter{Modules: rdb.NewModuleDAO(model.DB), Predictions: rdb.NewPredictionDAO(model.DB), RunStates: rdb.NewRunStateDAO(model.DB), Config: rdb.NewSysConfigDAO(model.DB), Fresh: forecastService}
	v1Group.GET("/operator/modules", runtimeHandler.OperatorModules)
	v1Group.GET("/operator/settings", runtimeHandler.OperatorSettings)
	v1Group.GET("/dashboard", forecastHandler.Dashboard)
	assistantHandler := &v1.AssistantRouter{Service: assistant.NewService(&assistant.Service{DB: model.DB, Forecast: forecastService, Modules: rdb.NewModuleDAO(model.DB),
		Predictions: rdb.NewPredictionDAO(model.DB), Config: configHandler.DAO, Database: databaseDriver()})}
	// the assistant reads its settings through the DAO the settings page writes with: a change is seen at once, not after the cache of another DAO expires
	v1Group.GET("/assistant/status", assistantHandler.Status)
	v1Group.POST("/assistant/chat", assistantHandler.Chat)
	v1Group.GET("/assistant/export", assistantHandler.Export)
	v1Group.GET("/operator/decisions", runtimeHandler.Decision)
	v1Group.POST("/operator/reports", runtimeHandler.Report)
	v1Group.GET("/admission/recommendations", runtimeHandler.Admission)
	v1Group.GET("/scaling-decisions", runtimeHandler.ListExecutions)
	v1Group.GET("/scale-outcomes", runtimeHandler.ListScaleOutcomes)

	ldapHandler := &v1.LDAPRouter{Service: ldapService}
	configHandler.LDAP = ldapHandler
	ldapGroup := v1Group.Group("/ldap")
	ldapGroup.GET("/settings", ldapHandler.GetSettings)
	ldapGroup.PUT("/settings", ldapHandler.UpdateSettings)
	ldapGroup.POST("/test", ldapHandler.Test)
	ldapGroup.POST("/sync", ldapHandler.Sync)

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

// databaseDriver names the database in use (sqlite, mysql, postgres), "" before one is open.
func databaseDriver() string {
	if model.DB == nil || model.DB.Dialector == nil {
		return ""
	}
	return model.DB.Dialector.Name()
}
