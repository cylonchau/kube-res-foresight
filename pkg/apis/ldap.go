package apis

// LDAPSettingDTO is the directory configuration. The bind password and the CA bundle body are never returned;
// has_bind_password and has_ca_cert say whether they are stored.
type LDAPSettingDTO struct {
	Enabled         bool   `json:"enabled"`
	URL             string `json:"url"`
	StartTLS        bool   `json:"start_tls"`
	SkipVerify      bool   `json:"skip_verify"`
	HasCACert       bool   `json:"has_ca_cert"`
	BindDN          string `json:"bind_dn"`
	HasBindPassword bool   `json:"has_bind_password"`
	UserBaseDN      string `json:"user_base_dn"`
	UserFilter      string `json:"user_filter"`
	DisplayNameAttr string `json:"display_name_attr"`
	EmailAttr       string `json:"email_attr"`
	PhoneAttr       string `json:"phone_attr"`
	GroupBaseDN     string `json:"group_base_dn"`
	GroupFilter     string `json:"group_filter"`
	GroupNameAttr   string `json:"group_name_attr"`
	MemberOfAttr    string `json:"member_of_attr"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
	DefaultRoleCode string `json:"default_role_code"`
	// GroupRoleMappings is one "directory group=role code" per line.
	GroupRoleMappings string `json:"group_role_mappings"`
}

// LDAPSettingReq replaces the configuration. A blank BindPassword or CACert keeps the stored value;
// ClearCACert removes the CA bundle. Enabling requires a successful connection check.
type LDAPSettingReq struct {
	Enabled           bool   `json:"enabled"`
	URL               string `json:"url"`
	StartTLS          bool   `json:"start_tls"`
	SkipVerify        bool   `json:"skip_verify"`
	CACert            string `json:"ca_cert"`
	ClearCACert       bool   `json:"clear_ca_cert"`
	BindDN            string `json:"bind_dn"`
	BindPassword      string `json:"bind_password"`
	UserBaseDN        string `json:"user_base_dn"`
	UserFilter        string `json:"user_filter"`
	DisplayNameAttr   string `json:"display_name_attr"`
	EmailAttr         string `json:"email_attr"`
	PhoneAttr         string `json:"phone_attr"`
	GroupBaseDN       string `json:"group_base_dn"`
	GroupFilter       string `json:"group_filter"`
	GroupNameAttr     string `json:"group_name_attr"`
	MemberOfAttr      string `json:"member_of_attr"`
	TimeoutSeconds    int    `json:"timeout_seconds"`
	DefaultRoleCode   string `json:"default_role_code"`
	GroupRoleMappings string `json:"group_role_mappings"`
}

// LDAPTestReq checks the saved configuration; with Username it also resolves that user (no password needed).
type LDAPTestReq struct {
	Username string `json:"username"`
}

// LDAPTestResult is the outcome of a connection test.
type LDAPTestResult struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	User  *LDAPTestedUser `json:"user,omitempty"`
}

// LDAPTestedUser is what the directory knows about the sample user and the roles it would receive.
type LDAPTestedUser struct {
	DN          string   `json:"dn"`
	DisplayName string   `json:"display_name"`
	Email       string   `json:"email"`
	Groups      []string `json:"groups"`
	Disabled    bool     `json:"disabled"`
	Roles       []string `json:"roles"`
}
