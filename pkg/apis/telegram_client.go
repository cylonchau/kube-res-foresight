package apis

type TelegramClientAccountListReq struct {
	ListOptions
	Enabled    *bool `form:"enabled"`
	Authorized *bool `form:"authorized"`
}

type TelegramClientAccountCreateReq struct {
	Name        string `json:"name" binding:"required"`
	PhoneNumber string `json:"phone_number" binding:"required"`
	APIID       int    `json:"api_id" binding:"required"`
	APIHash     string `json:"api_hash" binding:"required"`
	Enabled     *bool  `json:"enabled"`
}

type TelegramClientAccountUpdateReq struct {
	Name        *string `json:"name"`
	PhoneNumber *string `json:"phone_number"`
	APIID       *int    `json:"api_id"`
	APIHash     *string `json:"api_hash"`
	Enabled     *bool   `json:"enabled"`
}

type TelegramClientSendCodeReq struct {
	AccountID int64 `json:"account_id" binding:"required"`
}
type TelegramClientVerifyCodeReq struct {
	AccountID int64  `json:"account_id" binding:"required"`
	Code      string `json:"code" binding:"required"`
	Password  string `json:"password"`
}

type TelegramClientSubscriptionListReq struct {
	ListOptions
	AccountID int64  `form:"account_id"`
	Enabled   *bool  `form:"enabled"`
	State     string `form:"state"`
}

// TelegramClientSubscriptionCreateReq joins a channel with a Client Account.
// ChannelLink is a public username, @username or t.me link.
type TelegramClientSubscriptionCreateReq struct {
	AccountID   int64  `json:"account_id"`
	ChannelLink string `json:"channel_link"`
	Join        bool   `json:"join"`
}

type TelegramClientSubscriptionUpdateReq struct {
	Keywords []string `json:"keywords"`
	Enabled  *bool    `json:"enabled"`
}

type TelegramClientMessageListReq struct {
	ListOptions
	AccountID      int64  `form:"account_id"`
	SubscriptionID int64  `form:"subscription_id"`
	SourceType     string `form:"source_type"`
	Status         string `form:"status"`
}

// TelegramMessageBatchReq applies one action to cached messages: delete discards
// them, retry forwards them again, consume marks them consumed without forwarding.
type TelegramMessageBatchReq struct {
	IDs    []int64 `json:"ids" binding:"required,min=1"`
	Action string  `json:"action" binding:"required,oneof=delete retry consume"`
}
