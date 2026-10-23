package apis

type TelegramBotListReq struct {
	ListOptions
	Enabled *bool  `form:"enabled"`
	Status  string `form:"status"`
}

type TelegramBotCreateReq struct {
	Name       string  `json:"name" binding:"required"`
	Token      string  `json:"token" binding:"required"`
	Username   *string `json:"username"`
	Mode       *string `json:"mode"`
	APIBaseURL *string `json:"api_base_url"`
	MiniAppURL *string `json:"mini_app_url"`
	Enabled    *bool   `json:"enabled"`
}

type TelegramBotUpdateReq struct {
	Name       *string `json:"name"`
	Token      *string `json:"token"`
	Username   *string `json:"username"`
	Mode       *string `json:"mode"`
	APIBaseURL *string `json:"api_base_url"`
	MiniAppURL *string `json:"mini_app_url"`
	Enabled    *bool   `json:"enabled"`
}

type TelegramAgentListReq struct {
	ListOptions
	Online *bool `form:"online"`
}

type TelegramIDsReq struct {
	IDs []int64 `json:"ids" binding:"required,min=1"`
}

type TelegramBatchEnableReq struct {
	IDs     []int64 `json:"ids" binding:"required,min=1"`
	Enabled bool    `json:"enabled"`
}

type TelegramForwardRuleListReq struct {
	ListOptions
	AccountID      int64  `form:"account_id"`
	SubscriptionID int64  `form:"subscription_id"`
	SourceType     string `form:"source_type"`
	TargetClass    string `form:"target_class"`
	TargetKind     string `form:"target_kind"`
	Enabled        *bool  `form:"enabled"`
}

type TelegramForwardRuleReq struct {
	Name            string `json:"name"`
	AccountID       int64  `json:"account_id"`
	SubscriptionID  int64  `json:"subscription_id"`
	SourceType      string `json:"source_type"`
	Pattern         string `json:"pattern"`
	TargetKind      string `json:"target_kind"`
	TargetRef       string `json:"target_ref"`
	TargetRecipient string `json:"target_recipient"`
	ChannelID       int64  `json:"channel_id"`
	SendAccountID   int64  `json:"send_account_id"`
	Enabled         *bool  `json:"enabled"`
	Remark          string `json:"remark"`
}

type TelegramForwardChannelListReq struct {
	ListOptions
	Transport string `form:"transport"`
	Consumer  string `form:"consumer"`
	Enabled   *bool  `form:"enabled"`
}

// TelegramForwardChannelReq creates or edits a forward channel. The passwords are write-only: empty
// keeps the stored one.
type TelegramForwardChannelReq struct {
	Name            string `json:"name"`
	Transport       string `json:"transport"`
	RedisAddr       string `json:"redis_addr"`
	RedisDB         int    `json:"redis_db"`
	RedisPassword   string `json:"redis_password"`
	Stream          string `json:"stream"`
	Brokers         string `json:"brokers"`
	Topic           string `json:"topic"`
	KafkaUsername   string `json:"kafka_username"`
	KafkaPassword   string `json:"kafka_password"`
	KafkaTLS        bool   `json:"kafka_tls"`
	Format          string `json:"format"`
	ConsumerKind    string `json:"consumer_kind"`
	ConsumerRef     string `json:"consumer_ref"`
	ConsumerGroup   string `json:"consumer_group"`
	ConsumerEnabled *bool  `json:"consumer_enabled"`
	Enabled         *bool  `json:"enabled"`
	Remark          string `json:"remark"`
	ClearSecrets    bool   `json:"clear_secrets"`
}
