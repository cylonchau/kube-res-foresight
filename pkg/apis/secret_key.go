package apis

import "time"

// SecretKeyDTO omits AccessKeyHash and EncryptedValue (tagged json:"-" on the
// model since they're sensitive); AccessKey/AccessSecret are only ever
// populated transiently right after creation/rotation.
type SecretKeyDTO struct {
	ID            int64      `json:"id"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	Name          string     `json:"name"`
	KeyType       string     `json:"key_type"`
	MaskedValue   string     `json:"masked_value"`
	Status        bool       `json:"status"`
	Remark        string     `json:"remark"`
	AccessKey     string     `json:"access_key,omitempty"`
	AccessSecret  string     `json:"access_secret,omitempty"`
	ServerEnabled bool       `json:"server_enabled"`
	PermissionIDs []uint     `json:"permission_ids"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}

type SecretKeyCreateReq struct {
	Name          string     `json:"name" binding:"required"`
	KeyType       string     `json:"key_type"`
	ServerEnabled bool       `json:"server_enabled"`
	PermissionIDs []uint     `json:"permission_ids"`
	ExpiresAt     *time.Time `json:"expires_at"`
	Status        *bool      `json:"status"`
	Remark        string     `json:"remark"`
}

type SecretKeyUpdateReq struct {
	Name           *string    `json:"name"`
	KeyType        *string    `json:"key_type"`
	ServerEnabled  *bool      `json:"server_enabled"`
	PermissionIDs  *[]uint    `json:"permission_ids"`
	ExpiresAt      *time.Time `json:"expires_at"`
	ClearExpiresAt bool       `json:"clear_expires_at"`
	RotateSecret   bool       `json:"rotate_secret"`
	Status         *bool      `json:"status"`
	Remark         *string    `json:"remark"`
}

type SecretKeyListReq struct {
	ListOptions
	KeyType string  `form:"key_type" json:"key_type"`
	Status  *string `form:"status" json:"status"`
}

type DeveloperAccessCredential struct {
	AccessKey        string     `json:"access_key"`
	AccessSecretHash string     `json:"access_secret_hash,omitempty"`
	AccessSecret     string     `json:"access_secret,omitempty"` // legacy read compatibility only
	ServerEnabled    bool       `json:"server_enabled"`
	PermissionIDs    []uint     `json:"permission_ids,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
}
