package model

// TelegramUser represents the user JSON payload in Telegram Mini App initData
// and in a bot update's sender field.
//
// It lives here rather than in pkg/telegram because pkg/dao/rdb needs it in
// TelegramBindingDAO's signatures: with it defined in pkg/telegram, rdb had
// to import pkg/telegram, which made the reverse import a cycle and forced
// pkg/telegram to declare a local interface for every DAO it touched and to
// have its wiring done by an outside package. pkg/model is imported by both,
// so defining the plain data type here breaks that. pkg/telegram keeps a
// `TelegramUser = model.TelegramUser` alias so existing references stay
// valid and identical in type.
type TelegramUser struct {
	ID              int64  `json:"id"`
	IsBot           bool   `json:"is_bot,omitempty"`
	FirstName       string `json:"first_name"`
	LastName        string `json:"last_name,omitempty"`
	Username        string `json:"username,omitempty"`
	LanguageCode    string `json:"language_code,omitempty"`
	IsPremium       bool   `json:"is_premium,omitempty"`
	AllowsWriteToPm bool   `json:"allows_write_to_pm,omitempty"`
	PhotoURL        string `json:"photo_url,omitempty"`
}
