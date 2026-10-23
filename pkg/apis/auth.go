package apis

type LoginReq struct {
	Username    string `json:"username" binding:"required"`
	Password    string `json:"password" binding:"required"`
	OtpCode     string `json:"otp_code"`
	CaptchaID   string `json:"captcha_id"`
	CaptchaCode string `json:"captcha_code"`
	// Force, when true, revokes any other active web session for this
	// account before issuing tokens instead of returning ErrSessionConflict.
	// The client sets it after the user confirms a "kick the other session?"
	// prompt shown in response to that conflict.
	Force bool `json:"force"`
}

type LoginOTPReq struct {
	Ticket  string `json:"login_ticket" binding:"required"`
	OtpCode string `json:"otp_code" binding:"required"`
	Force   bool   `json:"force"`
}

type RefreshTokenReq struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

type LogoutReq struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

type UpdatePasswordReq struct {
	OldPassword string `json:"old_password" binding:"required"`
	Password    string `json:"password" binding:"required"`
}

type UpdateProfileReq struct {
	Email string `json:"email"`
	Phone string `json:"phone"`
}

type UpdateLanguageReq struct {
	// Language is the UI language code to remember for this account (e.g.
	// "zh-CN", "en-US"). Empty clears the preference, reverting the user
	// to system/browser language detection on next login.
	Language string `json:"language"`
}

type BindOTPReq struct {
	Secret string `json:"secret" binding:"required"`
	Code   string `json:"code" binding:"required"`
}

type UnbindOTPReq struct {
	Code string `json:"code" binding:"required"`
}
