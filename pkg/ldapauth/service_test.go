package ldapauth

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/cylonchau/kubeforecast/pkg/model"
	cryptoutils "github.com/cylonchau/kubeforecast/pkg/utils"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// fakeDirectory answers from a map, so the account logic is tested without sockets.
type fakeDirectory struct {
	users     map[string]fakeUser
	reachable bool
	lookups   int
}

type fakeUser struct {
	password string
	id       Identity
}

func (f *fakeDirectory) Authenticate(_ context.Context, _ Settings, username, password string) (*Identity, error) {
	u, ok := f.users[username]
	if !ok {
		return nil, ErrUserNotFound
	}
	if password == "" || u.password != password {
		return nil, ErrInvalidCredentials
	}
	id := u.id
	return &id, nil
}

func (f *fakeDirectory) Lookup(_ context.Context, _ Settings, username string) (*Identity, error) {
	f.lookups++
	if !f.reachable {
		return nil, errors.New("connection refused")
	}
	u, ok := f.users[username]
	if !ok {
		return nil, ErrUserNotFound
	}
	id := u.id
	return &id, nil
}

func (f *fakeDirectory) Check(context.Context, Settings) error {
	if !f.reachable {
		return errors.New("connection refused")
	}
	return nil
}

var dbSeq int32

type fixture struct {
	svc     *Service
	dir     *fakeDirectory
	db      *gorm.DB
	revoked []uint
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s_%d?mode=memory&cache=shared", t.Name(), atomic.AddInt32(&dbSeq, 1))), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Role{}, &model.Permission{}, &model.SysConfig{}))
	svc := &Service{DB: db}
	require.NoError(t, svc.Save(context.Background(), &model.LDAPSetting{Enabled: true, URL: "ldaps://ldap.example.org", UserBaseDN: "dc=example,dc=org", UserFilter: "(uid=%s)", TimeoutSeconds: 5,
		GroupRoleMappings: "sre=ADMIN\ndev=VIEWER\nghosts=OFF"}))
	for _, r := range []model.Role{{Name: "Admin", Code: "ADMIN", Status: true}, {Name: "Viewer", Code: "VIEWER", Status: true}, {Name: "Off", Code: "OFF", Status: false}} {
		role := r
		require.NoError(t, db.Create(&role).Error)
		if role.Code == "OFF" {
			require.NoError(t, db.Model(&role).Update("status", false).Error) // the column default is true, so false must be written explicitly
		}
	}
	f := &fixture{db: db, dir: &fakeDirectory{reachable: true, users: map[string]fakeUser{
		"alice": {"alice-pw", Identity{DN: "uid=alice,dc=example,dc=org", Username: "alice", DisplayName: "Alice A", Email: "alice@example.org", Groups: []string{"SRE", "dev"}}},
		"carol": {"carol-pw", Identity{DN: "uid=carol,dc=example,dc=org", Username: "carol", Groups: []string{"nobody"}}},
		"dave":  {"dave-pw", Identity{DN: "uid=dave,dc=example,dc=org", Username: "dave", Groups: []string{"dev"}, Disabled: true}},
		"erin":  {"erin-pw", Identity{DN: "uid=erin,dc=example,dc=org", Username: "erin", Groups: []string{"ghosts"}}},
	}}}
	f.svc = &Service{DB: db, Dir: f.dir, RevokeSessions: func(_ context.Context, id uint) error { f.revoked = append(f.revoked, id); return nil }}
	return f
}

func (f *fixture) user(t *testing.T, name string) model.User {
	t.Helper()
	var u model.User
	require.NoError(t, f.db.Preload("Roles").Where("username = ?", name).First(&u).Error)
	return u
}

func roleCodes(u model.User) []string {
	var codes []string
	for _, r := range u.Roles {
		codes = append(codes, r.Code)
	}
	return codes
}

func TestFirstLoginCreatesALocalMirrorWithRolesFromGroupMappings(t *testing.T) {
	f := newFixture(t)
	u, err := f.svc.Login(context.Background(), "alice", "alice-pw")
	require.NoError(t, err)
	assert.Equal(t, model.UserSourceLDAP, u.Source)
	stored := f.user(t, "alice")
	assert.Equal(t, "Alice A", stored.NickName)
	assert.Equal(t, "alice@example.org", stored.Email)
	assert.Equal(t, "uid=alice,dc=example,dc=org", stored.ExternalID)
	assert.True(t, stored.Status)
	assert.ElementsMatch(t, []string{"ADMIN", "VIEWER"}, roleCodes(stored), "group names match case-insensitively and roles are the union")
	assert.NotEmpty(t, stored.PasswordHash)
	assert.NotContains(t, stored.PasswordHash, "alice-pw")
}

func TestTheStoredPasswordHashNeverLogsAnLDAPUserInLocally(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Login(context.Background(), "alice", "alice-pw")
	require.NoError(t, err)
	_, err = f.svc.Login(context.Background(), "alice", "wrong")
	assert.ErrorIs(t, err, ErrInvalidCredentials)
	_, err = f.svc.Login(context.Background(), "alice", "")
	assert.ErrorIs(t, err, ErrInvalidCredentials)
}

func TestRolesAreRefreshedOnEveryLogin(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Login(context.Background(), "alice", "alice-pw")
	require.NoError(t, err)
	alice := f.dir.users["alice"]
	alice.id.Groups = []string{"dev"}
	f.dir.users["alice"] = alice
	_, err = f.svc.Login(context.Background(), "alice", "alice-pw")
	require.NoError(t, err)
	assert.Equal(t, []string{"VIEWER"}, roleCodes(f.user(t, "alice")), "leaving the sre group removes the admin role")
}

func TestAUserInNoMappedGroupIsDeniedUnlessThereIsADefaultRole(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Login(context.Background(), "carol", "carol-pw")
	assert.ErrorIs(t, err, ErrNoRole)
	var count int64
	require.NoError(t, f.db.Model(&model.User{}).Where("username = ?", "carol").Count(&count).Error)
	assert.Zero(t, count, "a denied user is not created")

	setConfig(t, f.db, "ldap.default_role_code", "VIEWER")
	_, err = f.svc.Login(context.Background(), "carol", "carol-pw")
	require.NoError(t, err)
	assert.Equal(t, []string{"VIEWER"}, roleCodes(f.user(t, "carol")))
}

func TestDisabledRolesDoNotGrantAccess(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Login(context.Background(), "erin", "erin-pw")
	assert.ErrorIs(t, err, ErrNoRole, "the only mapped role is disabled")
}

func TestADirectoryDisabledAccountIsDeniedAndItsSessionsEnd(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Login(context.Background(), "dave", "dave-pw")
	assert.ErrorIs(t, err, ErrDirectoryDisabled)

	// an account that was active, then disabled in the directory
	_, err = f.svc.Login(context.Background(), "alice", "alice-pw")
	require.NoError(t, err)
	aliceID := f.user(t, "alice").ID
	alice := f.dir.users["alice"]
	alice.id.Disabled = true
	f.dir.users["alice"] = alice
	_, err = f.svc.Login(context.Background(), "alice", "alice-pw")
	assert.ErrorIs(t, err, ErrDirectoryDisabled)
	stored := f.user(t, "alice")
	assert.False(t, stored.Status)
	assert.True(t, stored.ExternalDisabled)
	assert.Equal(t, []uint{aliceID}, f.revoked)
}

func TestAnAdministratorsDisableSticksButADirectoryDisableIsUndone(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Login(context.Background(), "alice", "alice-pw")
	require.NoError(t, err)

	require.NoError(t, f.db.Model(&model.User{}).Where("username = ?", "alice").Update("status", false).Error)
	_, err = f.svc.Login(context.Background(), "alice", "alice-pw")
	assert.ErrorIs(t, err, ErrAccountDisabled, "the directory does not override an administrator")

	require.NoError(t, f.db.Model(&model.User{}).Where("username = ?", "alice").Updates(map[string]any{"status": false, "external_disabled": true}).Error)
	_, err = f.svc.Login(context.Background(), "alice", "alice-pw")
	require.NoError(t, err, "a sync-disabled account returns when the directory enables it again")
	assert.True(t, f.user(t, "alice").Status)
}

func TestLocalAccountsAreNeverHandledByTheDirectory(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.db.Create(&model.User{Username: "admin", PasswordHash: "x", Status: true, Source: model.UserSourceLocal}).Error)
	f.dir.users["admin"] = fakeUser{"ldap-pw", Identity{Username: "admin", Groups: []string{"sre"}}}
	_, err := f.svc.Login(context.Background(), "admin", "ldap-pw")
	assert.ErrorIs(t, err, ErrLocalAccount, "the break-glass admin cannot be taken over by a directory entry of the same name")
	assert.Equal(t, model.UserSourceLocal, f.user(t, "admin").Source)

	// and the sync leaves it alone even if the directory is empty
	f.dir.users = map[string]fakeUser{}
	res, err := f.svc.SyncAll(context.Background())
	require.NoError(t, err)
	assert.Zero(t, res.Checked)
	assert.True(t, f.user(t, "admin").Status)
}

func TestLoginRequiresLDAPToBeEnabledAndValid(t *testing.T) {
	f := newFixture(t)
	setConfig(t, f.db, "ldap.enabled", "false")
	_, err := f.svc.Login(context.Background(), "alice", "alice-pw")
	assert.ErrorIs(t, err, ErrNotEnabled)
	assert.False(t, f.svc.Enabled(context.Background()))

	setConfig(t, f.db, "ldap.enabled", "true")
	assert.True(t, f.svc.Enabled(context.Background()))
	setConfig(t, f.db, "ldap.url", "ldap://plain.example.org")
	assert.False(t, f.svc.Enabled(context.Background()), "settings that would send passwords in clear text are never active")

	empty := newFixture(t)
	require.NoError(t, empty.db.Where("config_key LIKE ?", "ldap.%").Delete(&model.SysConfig{}).Error)
	_, err = empty.svc.Login(context.Background(), "alice", "alice-pw")
	assert.ErrorIs(t, err, ErrNotEnabled)
}

func TestOverlongLoginNamesAreRejectedBeforeTheDirectory(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Login(context.Background(), string(make([]byte, 80)), "x")
	assert.ErrorIs(t, err, ErrInvalidCredentials)
}

func TestSyncDisablesVanishedDisabledAndUnmappedAccountsAndReEnablesReturnedOnes(t *testing.T) {
	f := newFixture(t)
	for _, n := range []string{"alice"} {
		_, err := f.svc.Login(context.Background(), n, n+"-pw")
		require.NoError(t, err)
	}
	setConfig(t, f.db, "ldap.default_role_code", "VIEWER")
	_, err := f.svc.Login(context.Background(), "carol", "carol-pw")
	require.NoError(t, err)
	// frank: an ldap-source mirror whose directory entry is gone
	require.NoError(t, f.db.Create(&model.User{Username: "frank", PasswordHash: "x", Status: true, Source: model.UserSourceLDAP}).Error)
	// grace: disabled by an administrator, active in the directory
	require.NoError(t, f.db.Create(&model.User{Username: "grace", PasswordHash: "x", Status: false, Source: model.UserSourceLDAP}).Error)
	require.NoError(t, f.db.Model(&model.User{}).Where("username = ?", "grace").Update("status", false).Error)
	f.dir.users["grace"] = fakeUser{"g", Identity{Username: "grace", Groups: []string{"dev"}}}
	// the directory now disables alice and drops carol's default-role fallback
	alice := f.dir.users["alice"]
	alice.id.Disabled = true
	f.dir.users["alice"] = alice
	setConfig(t, f.db, "ldap.default_role_code", "")

	res, err := f.svc.SyncAll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 4, res.Checked)
	assert.Equal(t, 3, res.Disabled, "alice (disabled), carol (no role any more) and frank (gone)")
	for _, n := range []string{"alice", "carol", "frank"} {
		u := f.user(t, n)
		assert.False(t, u.Status, n)
		assert.True(t, u.ExternalDisabled, n)
	}
	assert.False(t, f.user(t, "grace").Status, "an administrator's disable is not undone by the sync")
	assert.Len(t, f.revoked, 3, "sessions of the disabled accounts end")

	// alice returns in the directory
	alice.id.Disabled = false
	f.dir.users["alice"] = alice
	res, err = f.svc.SyncAll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, res.Enabled)
	assert.True(t, f.user(t, "alice").Status)
}

func TestAnUnreachableDirectoryChangesNobody(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Login(context.Background(), "alice", "alice-pw")
	require.NoError(t, err)
	f.dir.reachable = false
	_, err = f.svc.SyncAll(context.Background())
	require.Error(t, err)
	assert.True(t, f.user(t, "alice").Status, "an outage must not lock everyone out")
	assert.Empty(t, f.revoked)
}

func TestSyncDoesNothingWhileLDAPIsOff(t *testing.T) {
	f := newFixture(t)
	setConfig(t, f.db, "ldap.enabled", "false")
	res, err := f.svc.SyncAll(context.Background())
	require.NoError(t, err)
	assert.Zero(t, res.Checked)
	assert.Zero(t, f.dir.lookups)
}

func TestRuntimeSettingsDecryptsTheBindPassword(t *testing.T) {
	encrypted, err := cryptoutils.EncryptValue("s3cret")
	require.NoError(t, err)
	rt, err := RuntimeSettings(&model.LDAPSetting{URL: "ldaps://x", BindPassword: encrypted, TimeoutSeconds: 3})
	require.NoError(t, err)
	assert.Equal(t, "s3cret", rt.BindPassword)
	assert.EqualValues(t, 3, rt.Timeout.Seconds())
	_, err = RuntimeSettings(&model.LDAPSetting{BindPassword: "not-encrypted"})
	assert.Error(t, err)
}

// setConfig overwrites one ldap.* system parameter.
func setConfig(t *testing.T, db *gorm.DB, key, value string) {
	t.Helper()
	require.NoError(t, db.Model(&model.SysConfig{}).Where("config_key = ?", key).Update("config_value", value).Error)
}
