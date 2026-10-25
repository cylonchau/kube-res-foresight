package ldapauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	apputils "github.com/cylonchau/kube-resource-fcst/pkg/app/utils"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	cryptoutils "github.com/cylonchau/kube-resource-fcst/pkg/utils"
	"gorm.io/gorm"
)

// Errors of the login flow.
var (
	ErrNotEnabled        = errors.New("ldap login is not enabled")
	ErrNoRole            = errors.New("ldap: the user belongs to no group mapped to a role")
	ErrDirectoryDisabled = errors.New("ldap: the account is disabled in the directory")
	ErrAccountDisabled   = errors.New("ldap: the account was disabled by an administrator")
	ErrLocalAccount      = errors.New("ldap: a local account with this name exists")
)

// maxUsernameLength matches the user table.
const maxUsernameLength = 50

// Service ties the directory to the user, role and mapping tables.
type Service struct {
	DB  *gorm.DB
	Dir Directory
	// RevokeSessions ends every session of a user; it is called when the directory disables an account.
	RevokeSessions func(ctx context.Context, userID uint) error
}

// Load returns the stored configuration and its runtime form; (nil, zero) when none is saved yet.
func (s *Service) Load(ctx context.Context) (*model.LDAPSetting, Settings, error) {
	row, found, err := loadStored(ctx, s.DB)
	if err != nil {
		return nil, Settings{}, err
	}
	if !found {
		return nil, Settings{}, nil
	}
	rt, err := RuntimeSettings(row)
	return row, rt, err
}

// RuntimeSettings converts the stored row, decrypting the bind password.
func RuntimeSettings(row *model.LDAPSetting) (Settings, error) {
	s := Settings{
		URL: row.URL, StartTLS: row.StartTLS, SkipVerify: row.SkipVerify, CACertPEM: row.CACert, BindDN: row.BindDN,
		UserBaseDN: row.UserBaseDN, UserFilter: row.UserFilter, DisplayNameAttr: row.DisplayNameAttr, EmailAttr: row.EmailAttr, PhoneAttr: row.PhoneAttr,
		GroupBaseDN: row.GroupBaseDN, GroupFilter: row.GroupFilter, GroupNameAttr: row.GroupNameAttr,
		Timeout: time.Duration(row.TimeoutSeconds) * time.Second,
	}
	if row.BindPassword != "" {
		pw, err := cryptoutils.DecryptValue(row.BindPassword)
		if err != nil {
			return s, errors.New("the stored bind password cannot be decrypted; enter it again")
		}
		s.BindPassword = pw
	}
	return s, nil
}

// Enabled reports whether directory login is switched on and usable.
func (s *Service) Enabled(ctx context.Context) bool {
	row, rt, err := s.Load(ctx)
	return err == nil && row != nil && row.Enabled && rt.Validate() == nil
}

type mappings struct {
	roleCodesByGroup map[string][]string
	defaultRole      string
}

func (s *Service) mappings(ctx context.Context, row *model.LDAPSetting) (*mappings, error) {
	list, err := ParseMappings(row.GroupRoleMappings)
	if err != nil {
		return nil, fmt.Errorf("ldap.group_role_mappings: %w", err)
	}
	m := &mappings{roleCodesByGroup: map[string][]string{}, defaultRole: row.DefaultRoleCode}
	for _, r := range list {
		key := strings.ToLower(strings.TrimSpace(r.Group))
		m.roleCodesByGroup[key] = append(m.roleCodesByGroup[key], r.Role)
	}
	return m, nil
}

// roles resolves the enabled roles an identity is entitled to: the union of the roles of its groups, else the
// default role. An empty result means "no access".
func (s *Service) roles(ctx context.Context, m *mappings, id *Identity) ([]model.Role, error) {
	codes := map[string]struct{}{}
	for _, g := range id.Groups {
		for _, code := range m.roleCodesByGroup[strings.ToLower(strings.TrimSpace(g))] {
			codes[code] = struct{}{}
		}
	}
	if len(codes) == 0 && m.defaultRole != "" {
		codes[m.defaultRole] = struct{}{}
	}
	if len(codes) == 0 {
		return nil, nil
	}
	list := make([]string, 0, len(codes))
	for c := range codes {
		list = append(list, c)
	}
	var roles []model.Role
	err := s.DB.WithContext(ctx).Where("code IN ? AND status = ?", list, true).Find(&roles).Error
	return roles, err
}

func randomPasswordHash() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return apputils.HashPassword(hex.EncodeToString(buf))
}

func (s *Service) revoke(ctx context.Context, userID uint) {
	if s.RevokeSessions != nil {
		_ = s.RevokeSessions(ctx, userID)
	}
}

// disable marks a directory account disabled and ends its sessions.
func (s *Service) disable(ctx context.Context, u *model.User) error {
	if err := s.DB.WithContext(ctx).Model(u).Updates(map[string]any{"status": false, "external_disabled": true}).Error; err != nil {
		return err
	}
	s.revoke(ctx, u.ID)
	return nil
}

// Login verifies the password against the directory and returns the local user, created on first login. Local
// accounts are never handled here (they are the break-glass path), and a directory failure never affects them.
func (s *Service) Login(ctx context.Context, username, password string) (*model.User, error) {
	username = strings.TrimSpace(username)
	row, rt, err := s.Load(ctx)
	if err != nil {
		return nil, err
	}
	if row == nil || !row.Enabled {
		return nil, ErrNotEnabled
	}
	if len(username) > maxUsernameLength {
		return nil, ErrInvalidCredentials
	}
	var existing model.User
	found := true
	if err := s.DB.WithContext(ctx).Where("username = ?", username).First(&existing).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		found = false
	}
	if found && existing.Source != model.UserSourceLDAP {
		return nil, ErrLocalAccount
	}
	if found && !existing.Status && !existing.ExternalDisabled {
		return nil, ErrAccountDisabled
	}

	id, err := s.Dir.Authenticate(ctx, rt, username, password)
	if err != nil {
		return nil, err
	}
	if id.Disabled {
		if found {
			if err := s.disable(ctx, &existing); err != nil {
				return nil, err
			}
		}
		return nil, ErrDirectoryDisabled
	}
	m, err := s.mappings(ctx, row)
	if err != nil {
		return nil, err
	}
	roles, err := s.roles(ctx, m, id)
	if err != nil {
		return nil, err
	}
	if len(roles) == 0 {
		if found {
			if err := s.disable(ctx, &existing); err != nil {
				return nil, err
			}
		}
		return nil, ErrNoRole
	}
	return s.upsert(ctx, found, &existing, username, id, roles)
}

// PreviewRoles lists the role codes an identity would receive, for the administrator's connection test.
func (s *Service) PreviewRoles(ctx context.Context, id *Identity) ([]string, error) {
	row, _, err := s.Load(ctx)
	if err != nil || row == nil {
		return nil, err
	}
	m, err := s.mappings(ctx, row)
	if err != nil {
		return nil, err
	}
	roles, err := s.roles(ctx, m, id)
	codes := make([]string, 0, len(roles))
	for _, r := range roles {
		codes = append(codes, r.Code)
	}
	return codes, err
}

func (s *Service) upsert(ctx context.Context, found bool, existing *model.User, username string, id *Identity, roles []model.Role) (*model.User, error) {
	display := id.DisplayName
	if display == "" {
		display = username
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if !found {
			hash, err := randomPasswordHash()
			if err != nil {
				return err
			}
			*existing = model.User{Username: username, PasswordHash: hash, NickName: clip(display, 50), Email: clip(id.Email, 100), Phone: clip(id.Phone, 20),
				Status: true, Source: model.UserSourceLDAP, ExternalID: clip(id.DN, 512)}
			if err := tx.Create(existing).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Model(existing).Updates(map[string]any{
				"nick_name": clip(display, 50), "email": clip(id.Email, 100), "phone": clip(id.Phone, 20),
				"external_id": clip(id.DN, 512), "status": true, "external_disabled": false,
			}).Error; err != nil {
				return err
			}
		}
		return tx.Model(existing).Association("Roles").Replace(roles)
	})
	if err != nil {
		return nil, err
	}
	existing.Status, existing.ExternalDisabled = true, false
	existing.Roles = roles
	return existing, nil
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// SyncResult counts what a directory sync changed.
type SyncResult struct {
	Checked   int `json:"checked"`
	Disabled  int `json:"disabled"`
	Enabled   int `json:"enabled"`
	Refreshed int `json:"refreshed"`
}

// SyncAll re-checks every directory account: an account that vanished, was disabled or lost all its group
// mappings is disabled and loses its sessions; one the directory enabled again is re-enabled unless an
// administrator disabled it. If the directory cannot be reached nobody is changed: an outage must not lock
// everyone out.
func (s *Service) SyncAll(ctx context.Context) (*SyncResult, error) {
	row, rt, err := s.Load(ctx)
	if err != nil {
		return nil, err
	}
	if row == nil || !row.Enabled {
		return &SyncResult{}, nil
	}
	if err := s.Dir.Check(ctx, rt); err != nil {
		return nil, fmt.Errorf("directory unreachable, no account was changed: %w", err)
	}
	m, err := s.mappings(ctx, row)
	if err != nil {
		return nil, err
	}
	var users []model.User
	if err := s.DB.WithContext(ctx).Where("source = ?", model.UserSourceLDAP).Find(&users).Error; err != nil {
		return nil, err
	}
	res := &SyncResult{}
	for i := range users {
		u := &users[i]
		res.Checked++
		id, err := s.Dir.Lookup(ctx, rt, u.Username)
		active := err == nil && !id.Disabled
		var roles []model.Role
		if active {
			if roles, err = s.roles(ctx, m, id); err != nil {
				return res, err
			}
			active = len(roles) > 0
		}
		switch {
		case err != nil && !errors.Is(err, ErrUserNotFound):
			return res, err // an unexpected directory error: stop, do not guess
		case !active:
			if u.Status { // an account already off (by sync or by an administrator) is left as it is
				if err := s.disable(ctx, u); err != nil {
					return res, err
				}
				res.Disabled++
			}
		default:
			if !u.Status && !u.ExternalDisabled {
				continue // disabled by an administrator: stays disabled
			}
			wasDisabled := !u.Status
			if _, err := s.upsert(ctx, true, u, u.Username, id, roles); err != nil {
				return res, err
			}
			if wasDisabled {
				res.Enabled++
			} else {
				res.Refreshed++
			}
		}
	}
	return res, nil
}
