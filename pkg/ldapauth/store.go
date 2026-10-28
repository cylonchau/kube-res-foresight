package ldapauth

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	cryptoutils "github.com/cylonchau/kube-resource-fcst/pkg/utils"
)

// The directory configuration lives in the system parameters, under the ldap. prefix. The generic settings API
// never returns or accepts these keys (see IsInternalSysConfigKey): they are read and written only through the
// LDAP panel of the system parameters page, which validates them, encrypts the secrets and never echoes them.
const (
	KeyPrefix = "ldap."

	keyEnabled         = "ldap.enabled"
	keyURL             = "ldap.url"
	keyStartTLS        = "ldap.start_tls"
	keySkipVerify      = "ldap.skip_verify"
	keyCACert          = "ldap.ca_cert"
	keyBindDN          = "ldap.bind_dn"
	keyBindPassword    = "ldap.bind_password"
	keyUserBaseDN      = "ldap.user_base_dn"
	keyUserFilter      = "ldap.user_filter"
	keyDisplayNameAttr = "ldap.display_name_attr"
	keyEmailAttr       = "ldap.email_attr"
	keyPhoneAttr       = "ldap.phone_attr"
	keyGroupBaseDN     = "ldap.group_base_dn"
	keyGroupFilter     = "ldap.group_filter"
	keyGroupNameAttr   = "ldap.group_name_attr"
	keyTimeout         = "ldap.timeout_seconds"
	keyDefaultRole     = "ldap.default_role_code"
	// KeyGroupMappings holds one "directory group=role code" per line.
	KeyGroupMappings = "ldap.group_role_mappings"
)

// Mapping is one directory group granting one role.
type Mapping struct {
	Group string `json:"group_name"`
	Role  string `json:"role_code"`
}

// ParseMappings reads the multi-line mapping setting. Blank lines and lines starting with # are ignored; a line
// is "group=role". A group may appear on several lines to grant several roles.
func ParseMappings(text string) ([]Mapping, error) {
	var out []Mapping
	seen := map[string]bool{}
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		at := strings.LastIndex(line, "=")
		if at <= 0 || at == len(line)-1 {
			return nil, fmt.Errorf("line %d: expected \"directory group=role code\"", i+1)
		}
		m := Mapping{Group: strings.TrimSpace(line[:at]), Role: strings.TrimSpace(line[at+1:])}
		if m.Group == "" || m.Role == "" {
			return nil, fmt.Errorf("line %d: expected \"directory group=role code\"", i+1)
		}
		key := strings.ToLower(m.Group) + "=" + m.Role
		if seen[key] {
			return nil, fmt.Errorf("line %d: the group is already mapped to this role", i+1)
		}
		seen[key] = true
		out = append(out, m)
	}
	return out, nil
}

func b(v bool) string { return strconv.FormatBool(v) }

func configNames() map[string]string {
	return map[string]string{
		keyEnabled: "LDAP Enabled", keyURL: "LDAP URL", keyStartTLS: "LDAP StartTLS", keySkipVerify: "LDAP Skip TLS Verify",
		keyCACert: "LDAP CA Certificate", keyBindDN: "LDAP Bind DN", keyBindPassword: "LDAP Bind Password",
		keyUserBaseDN: "LDAP User Base DN", keyUserFilter: "LDAP User Filter", keyDisplayNameAttr: "LDAP Display Name Attribute",
		keyEmailAttr: "LDAP Email Attribute", keyPhoneAttr: "LDAP Phone Attribute", keyGroupBaseDN: "LDAP Group Base DN",
		keyGroupFilter: "LDAP Group Filter", keyGroupNameAttr: "LDAP Group Name Attribute", keyTimeout: "LDAP Timeout Seconds",
		keyDefaultRole: "LDAP Default Role", KeyGroupMappings: "LDAP Group Role Mappings",
	}
}

func values(row *model.LDAPSetting) map[string]string {
	return map[string]string{
		keyEnabled: b(row.Enabled), keyURL: row.URL, keyStartTLS: b(row.StartTLS), keySkipVerify: b(row.SkipVerify), keyCACert: row.CACert,
		keyBindDN: row.BindDN, keyBindPassword: row.BindPassword, keyUserBaseDN: row.UserBaseDN, keyUserFilter: row.UserFilter,
		keyDisplayNameAttr: row.DisplayNameAttr, keyEmailAttr: row.EmailAttr, keyPhoneAttr: row.PhoneAttr,
		keyGroupBaseDN: row.GroupBaseDN, keyGroupFilter: row.GroupFilter, keyGroupNameAttr: row.GroupNameAttr,
		keyTimeout: strconv.Itoa(row.TimeoutSeconds), keyDefaultRole: row.DefaultRoleCode, KeyGroupMappings: row.GroupRoleMappings,
	}
}

func fromValues(m map[string]string) *model.LDAPSetting {
	timeout, _ := strconv.Atoi(m[keyTimeout])
	truthy := func(k string) bool { v, _ := strconv.ParseBool(m[k]); return v }
	return &model.LDAPSetting{
		Enabled: truthy(keyEnabled), URL: m[keyURL], StartTLS: truthy(keyStartTLS), SkipVerify: truthy(keySkipVerify), CACert: m[keyCACert],
		BindDN: m[keyBindDN], BindPassword: m[keyBindPassword], UserBaseDN: m[keyUserBaseDN], UserFilter: m[keyUserFilter],
		DisplayNameAttr: m[keyDisplayNameAttr], EmailAttr: m[keyEmailAttr], PhoneAttr: m[keyPhoneAttr],
		GroupBaseDN: m[keyGroupBaseDN], GroupFilter: m[keyGroupFilter], GroupNameAttr: m[keyGroupNameAttr],
		TimeoutSeconds: timeout, DefaultRoleCode: m[keyDefaultRole], GroupRoleMappings: m[KeyGroupMappings],
	}
}

// loadStored reads the ldap.* parameters; found is false when none has ever been saved.
func loadStored(ctx context.Context, db *gorm.DB) (*model.LDAPSetting, bool, error) {
	var rows []model.SysConfig
	if err := db.WithContext(ctx).Where("config_key LIKE ?", KeyPrefix+"%").Find(&rows).Error; err != nil {
		return nil, false, err
	}
	if len(rows) == 0 {
		return nil, false, nil
	}
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		m[r.ConfigKey] = r.ConfigValue
	}
	return fromValues(m), true, nil
}

// Save writes the whole configuration in one transaction.
func (s *Service) Save(ctx context.Context, row *model.LDAPSetting) error {
	if row == nil {
		return errors.New("nothing to save")
	}
	names := configNames()
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for key, value := range values(row) {
			cfg := model.SysConfig{ConfigKey: key, ConfigValue: value, ConfigName: names[key], Remark: "managed by the LDAP panel of the system parameters"}
			err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "config_key"}},
				DoUpdates: clause.AssignmentColumns([]string{"config_value", "updated_at"}),
			}).Create(&cfg).Error
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// Secret reports whether the parameter holds a credential: the list shows a mask instead of its value.
func Secret(key string) bool { return key == keyBindPassword || key == keyCACert }

// DefaultConfigs are the ldap.* parameters a fresh installation starts with (directory login off), so they are
// listed and edited like any other system parameter.
func DefaultConfigs() []model.SysConfig {
	names := configNames()
	row := &model.LDAPSetting{UserFilter: "(&(objectClass=person)(uid=%s))", DisplayNameAttr: "cn", EmailAttr: "mail", GroupNameAttr: "cn", TimeoutSeconds: 10}
	vals := values(row)
	out := make([]model.SysConfig, 0, len(vals))
	for _, key := range orderedKeys {
		out = append(out, model.SysConfig{ConfigKey: key, ConfigValue: vals[key], ConfigName: names[key], Remark: "LDAP login: edit here; enabling checks the directory connection"})
	}
	return out
}

var orderedKeys = []string{keyEnabled, keyURL, keyStartTLS, keySkipVerify, keyCACert, keyBindDN, keyBindPassword, keyUserBaseDN, keyUserFilter,
	keyDisplayNameAttr, keyEmailAttr, keyPhoneAttr, keyGroupBaseDN, keyGroupFilter, keyGroupNameAttr, keyTimeout, keyDefaultRole, KeyGroupMappings}

// WithValue returns a copy of the stored configuration with one parameter changed. The bind password is encrypted;
// an unknown key, a non-boolean switch or a non-number timeout is refused.
func WithValue(row *model.LDAPSetting, key, value string) (*model.LDAPSetting, error) {
	m := values(row)
	if _, ok := m[key]; !ok {
		return nil, fmt.Errorf("%s is not an LDAP parameter", key)
	}
	value = strings.TrimSpace(value)
	switch key {
	case keyEnabled, keyStartTLS, keySkipVerify:
		if _, err := strconv.ParseBool(value); err != nil {
			return nil, fmt.Errorf("%s must be true or false", key)
		}
	case keyTimeout:
		if _, err := strconv.Atoi(value); err != nil {
			return nil, fmt.Errorf("%s must be a whole number of seconds", key)
		}
	case keyBindPassword:
		if value != "" {
			enc, err := cryptoutils.EncryptValue(value)
			if err != nil {
				return nil, err
			}
			value = enc
		}
	}
	m[key] = value
	out := fromValues(m)
	if out.BindDN == "" {
		out.BindPassword = ""
	}
	return out, nil
}
