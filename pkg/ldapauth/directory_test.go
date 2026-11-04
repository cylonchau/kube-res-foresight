package ldapauth

import (
	"context"
	"testing"
	"time"

	"github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (d *testDirectory) settings() Settings {
	return Settings{
		URL: "ldaps://" + d.addr, CACertPEM: d.caPEM,
		BindDN: "cn=svc,dc=example,dc=org", BindPassword: "svc-pw",
		UserBaseDN: "ou=people,dc=example,dc=org", UserFilter: "(&(objectClass=person)(uid=%s))",
		DisplayNameAttr: "cn", EmailAttr: "mail", PhoneAttr: "telephoneNumber",
		GroupBaseDN: "ou=groups,dc=example,dc=org", GroupFilter: "(&(objectClass=groupOfNames)(member=%s))", GroupNameAttr: "cn",
		Timeout: 5 * time.Second,
	}
}

func TestAuthenticateOverLDAPSReturnsTheIdentityAndGroups(t *testing.T) {
	d := newTestDirectory(t)
	id, err := Client{}.Authenticate(context.Background(), d.settings(), "alice", "alice-pw")
	require.NoError(t, err)
	assert.Equal(t, "uid=alice,ou=people,dc=example,dc=org", id.DN)
	assert.Equal(t, "Alice A", id.DisplayName)
	assert.Equal(t, "alice@example.org", id.Email)
	assert.Equal(t, "+1 555 0100", id.Phone)
	assert.ElementsMatch(t, []string{"sre", "dev"}, id.Groups)
	assert.False(t, id.Disabled)
}

func TestTheServerCertificateMustBeTrusted(t *testing.T) {
	d := newTestDirectory(t)
	s := d.settings()
	s.CACertPEM = ""
	_, err := Client{}.Authenticate(context.Background(), s, "alice", "alice-pw")
	require.Error(t, err, "a self-signed certificate is refused without its CA")
	assert.NotErrorIs(t, err, ErrInvalidCredentials)

	s.SkipVerify = true
	_, err = Client{}.Authenticate(context.Background(), s, "alice", "alice-pw")
	assert.NoError(t, err, "verification can be skipped only by an explicit setting")
}

func TestWrongAndEmptyPasswordsNeverAuthenticate(t *testing.T) {
	d := newTestDirectory(t)
	_, err := Client{}.Authenticate(context.Background(), d.settings(), "alice", "nope")
	assert.ErrorIs(t, err, ErrInvalidCredentials)

	_, err = Client{}.Authenticate(context.Background(), d.settings(), "alice", "")
	assert.ErrorIs(t, err, ErrInvalidCredentials)
	// the empty password was refused before any bind of the user's DN with an empty password
	d.mu.Lock()
	for _, b := range d.binds {
		assert.NotEqual(t, "uid=alice,ou=people,dc=example,dc=org|", b, "an empty-password bind would be an anonymous bind on many servers")
	}
	d.mu.Unlock()

	_, err = Client{}.Authenticate(context.Background(), d.settings(), "", "x")
	assert.ErrorIs(t, err, ErrInvalidCredentials)
}

func TestUnknownAmbiguousAndInjectedLoginNames(t *testing.T) {
	d := newTestDirectory(t)
	_, err := Client{}.Authenticate(context.Background(), d.settings(), "nobody", "x")
	assert.ErrorIs(t, err, ErrUserNotFound)

	_, err = Client{}.Authenticate(context.Background(), d.settings(), "dup", "x")
	assert.ErrorIs(t, err, ErrAmbiguousUser)

	// without escaping this would turn the filter into (uid=*)(uid=*) and match every user
	_, err = Client{}.Authenticate(context.Background(), d.settings(), "*)(uid=*", "x")
	assert.ErrorIs(t, err, ErrUserNotFound, "filter metacharacters in the login name are escaped")
	_, err = Client{}.Authenticate(context.Background(), d.settings(), "*", "x")
	assert.ErrorIs(t, err, ErrUserNotFound, "a wildcard is a literal, not a pattern")
}

func TestDisabledAccountsAreDetectedFromTheDirectory(t *testing.T) {
	d := newTestDirectory(t)
	bob, err := Client{}.Lookup(context.Background(), d.settings(), "bob")
	require.NoError(t, err)
	assert.True(t, bob.Disabled, "nsAccountLock=true")
	off, err := Client{}.Lookup(context.Background(), d.settings(), "ad-off")
	require.NoError(t, err)
	assert.True(t, off.Disabled, "userAccountControl bit 2 (ACCOUNTDISABLE)")
	alice, err := Client{}.Lookup(context.Background(), d.settings(), "alice")
	require.NoError(t, err)
	assert.False(t, alice.Disabled)
	assert.False(t, d.boundWith("uid=alice,ou=people,dc=example,dc=org"), "a lookup never binds as the user")
}

func TestCheckVerifiesTheServiceAccountAndUserBase(t *testing.T) {
	d := newTestDirectory(t)
	require.NoError(t, Client{}.Check(context.Background(), d.settings()))
	bad := d.settings()
	bad.BindPassword = "wrong"
	err := Client{}.Check(context.Background(), bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bind service account")
	unreachable := d.settings()
	unreachable.URL = "ldaps://127.0.0.1:1"
	assert.Error(t, Client{}.Check(context.Background(), unreachable))
}

func TestCanceledContextStopsBeforeConnecting(t *testing.T) {
	d := newTestDirectory(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Client{}.Authenticate(ctx, d.settings(), "alice", "alice-pw")
	assert.ErrorIs(t, err, context.Canceled)
}

func TestSettingsValidation(t *testing.T) {
	good := Settings{URL: "ldaps://ldap.example.org", UserBaseDN: "dc=example,dc=org", UserFilter: "(uid=%s)"}
	require.NoError(t, good.Validate())

	for name, mutate := range map[string]func(*Settings){
		"plain ldap without starttls": func(s *Settings) { s.URL = "ldap://ldap.example.org" },
		"unknown scheme":              func(s *Settings) { s.URL = "http://ldap.example.org" },
		"no host":                     func(s *Settings) { s.URL = "ldaps://" },
		"no base dn":                  func(s *Settings) { s.UserBaseDN = "" },
		"filter without placeholder":  func(s *Settings) { s.UserFilter = "(uid=bob)" },
		"filter with two":             func(s *Settings) { s.UserFilter = "(|(uid=%s)(mail=%s))" },
		"unbalanced filter":           func(s *Settings) { s.UserFilter = "(uid=%s" },
		"not a filter":                func(s *Settings) { s.UserFilter = "uid=%s" },
		"group base without filter":   func(s *Settings) { s.GroupBaseDN = "ou=groups,dc=example,dc=org" },
		"group filter without vars":   func(s *Settings) { s.GroupBaseDN, s.GroupFilter = "ou=g", "(cn=x)" },
		"bind dn without password":    func(s *Settings) { s.BindDN = "cn=svc,dc=example,dc=org" },
		"bad ca":                      func(s *Settings) { s.CACertPEM = "garbage" },
	} {
		s := good
		mutate(&s)
		assert.Error(t, s.Validate(), name)
	}
	starttls := good
	starttls.URL, starttls.StartTLS = "ldap://ldap.example.org", true
	assert.NoError(t, starttls.Validate())
}

func TestMemberOfGivesTheGroupsAsDNAndAsName(t *testing.T) {
	e := ldap.NewEntry("cn=alice,dc=example,dc=org", map[string][]string{"memberOf": {"cn=ops,ou=groups,dc=example,dc=org", "CN=Dev Team,OU=g,DC=example,DC=org"}})
	assert.Equal(t, []string{"cn=ops,ou=groups,dc=example,dc=org", "ops", "CN=Dev Team,OU=g,DC=example,DC=org", "Dev Team"}, memberOfGroups(e, "memberOf"))
	assert.Empty(t, memberOfGroups(e, ""), "nothing is read when the attribute is not set")
	assert.Empty(t, memberOfGroups(e, "isMemberOf"), "an attribute the entry does not have")
}
