package apis

type ConfigUpdateReq struct {
	ConfigValue string `json:"config_value" binding:"required"`
}
