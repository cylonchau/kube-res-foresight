package model

// LDAPSetting is the directory configuration. It is not a table: it is stored in the system parameters under the
// ldap. prefix (see pkg/ldapauth). The bind password is stored encrypted and never serialized. Plain ldap:// is only
// accepted together with StartTLS, so credentials never cross the network in clear text.
type LDAPSetting struct {
	Enabled    bool   `json:"enabled"`
	URL        string `json:"url"` // ldaps://host:636 or ldap://host:389 with StartTLS
	StartTLS   bool   `json:"start_tls"`
	SkipVerify bool   `json:"skip_verify"`
	CACert     string `json:"-"`

	BindDN       string `json:"bind_dn"`
	BindPassword string `json:"-"` // encrypted

	UserBaseDN      string `json:"user_base_dn"`
	UserFilter      string `json:"user_filter"` // %s = the escaped login name
	DisplayNameAttr string `json:"display_name_attr"`
	EmailAttr       string `json:"email_attr"`
	PhoneAttr       string `json:"phone_attr"`

	GroupBaseDN   string `json:"group_base_dn"`
	GroupFilter   string `json:"group_filter"` // %s = the escaped user DN, %u = the escaped login name
	GroupNameAttr string `json:"group_name_attr"`
	// MemberOfAttr is the attribute of the user entry that lists the DNs of its groups (memberOf); empty = only the group search above.
	MemberOfAttr string `json:"member_of_attr"`

	TimeoutSeconds  int    `json:"timeout_seconds"`
	DefaultRoleCode string `json:"default_role_code"` // role of users in no mapped group; empty = deny them

	// GroupRoleMappings is one "directory group=role code" per line.
	GroupRoleMappings string `json:"group_role_mappings"`
}
