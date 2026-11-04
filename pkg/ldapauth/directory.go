// Package ldapauth authenticates users against an LDAP directory (OpenLDAP, Active Directory or any LDAPv3
// server) and maps directory groups to roles. Passwords are only ever checked by the directory.
package ldapauth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
)

// Errors a caller can act on. They deliberately do not say whether the user or the password was wrong.
var (
	ErrInvalidCredentials = errors.New("ldap: invalid credentials")
	ErrUserNotFound       = errors.New("ldap: user not found")
	ErrAmbiguousUser      = errors.New("ldap: the user filter matches more than one entry")
)

// Settings is the runtime copy of the directory configuration (secrets already decrypted).
type Settings struct {
	URL          string
	StartTLS     bool
	SkipVerify   bool
	CACertPEM    string
	BindDN       string
	BindPassword string

	UserBaseDN      string
	UserFilter      string // %s = escaped login name
	DisplayNameAttr string
	EmailAttr       string
	PhoneAttr       string

	GroupBaseDN   string
	GroupFilter   string // %s = escaped user DN, %u = escaped login name
	GroupNameAttr string
	MemberOfAttr  string // attribute of the user entry listing its group DNs, for example memberOf

	Timeout time.Duration
}

// Identity is what the directory knows about a user.
type Identity struct {
	DN          string
	Username    string
	DisplayName string
	Email       string
	Phone       string
	Groups      []string
	// Disabled is true when the directory marks the account disabled or locked.
	Disabled bool
}

// Directory is the operations the platform needs from a directory; tests replace it.
type Directory interface {
	// Authenticate verifies the password against the directory and returns the identity.
	Authenticate(ctx context.Context, s Settings, username, password string) (*Identity, error)
	// Lookup finds a user without a password, with the service account.
	Lookup(ctx context.Context, s Settings, username string) (*Identity, error)
	// Check connects and binds with the service account and searches the user base.
	Check(ctx context.Context, s Settings) error
}

// Validate checks the settings for use. It rejects anything that would send credentials in clear text.
func (s Settings) Validate() error {
	u, err := url.Parse(strings.TrimSpace(s.URL))
	if err != nil || u.Host == "" {
		return errors.New("url must be ldaps://host[:port] or ldap://host[:port]")
	}
	switch u.Scheme {
	case "ldaps":
	case "ldap":
		if !s.StartTLS {
			return errors.New("ldap:// requires start_tls; otherwise use ldaps://. Passwords are never sent in clear text")
		}
	default:
		return errors.New("url scheme must be ldaps or ldap")
	}
	if strings.TrimSpace(s.UserBaseDN) == "" {
		return errors.New("user_base_dn is required")
	}
	if strings.Count(s.UserFilter, "%s") != 1 || !balanced(s.UserFilter) {
		return errors.New("user_filter must be a balanced LDAP filter containing %s exactly once, for example (&(objectClass=person)(uid=%s))")
	}
	if (s.GroupBaseDN != "") != (s.GroupFilter != "") {
		return errors.New("group_base_dn and group_filter must be set together")
	}
	if s.GroupFilter != "" {
		if !balanced(s.GroupFilter) || (!strings.Contains(s.GroupFilter, "%s") && !strings.Contains(s.GroupFilter, "%u")) {
			return errors.New("group_filter must be a balanced LDAP filter containing %s (user DN) or %u (login name)")
		}
	}
	if s.BindDN != "" && s.BindPassword == "" {
		return errors.New("bind_password is required with bind_dn")
	}
	if s.CACertPEM != "" {
		if _, err := certPool(s.CACertPEM); err != nil {
			return err
		}
	}
	return nil
}

func balanced(filter string) bool {
	depth := 0
	for _, r := range filter {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0 && strings.HasPrefix(strings.TrimSpace(filter), "(")
}

func certPool(pem string) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM([]byte(pem)) {
		return nil, errors.New("ca_cert contains no valid PEM certificate")
	}
	return pool, nil
}

// Client is the go-ldap implementation of Directory.
type Client struct{}

func (s Settings) timeout() time.Duration {
	if s.Timeout <= 0 {
		return 10 * time.Second
	}
	return s.Timeout
}

// dial opens a TLS-protected connection: ldaps:// directly, or ldap:// upgraded with StartTLS.
func (Client) dial(s Settings) (*ldap.Conn, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	u, _ := url.Parse(strings.TrimSpace(s.URL))
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = map[string]string{"ldaps": "636", "ldap": "389"}[u.Scheme]
	}
	cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: s.SkipVerify} //nolint:gosec // explicit administrator opt-in
	if s.CACertPEM != "" {
		pool, err := certPool(s.CACertPEM)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = pool
	}
	dialer := &net.Dialer{Timeout: s.timeout()}
	address := net.JoinHostPort(host, port)
	var conn *ldap.Conn
	var err error
	if u.Scheme == "ldaps" {
		conn, err = ldap.DialURL("ldaps://"+address, ldap.DialWithDialer(dialer), ldap.DialWithTLSConfig(cfg))
	} else {
		conn, err = ldap.DialURL("ldap://"+address, ldap.DialWithDialer(dialer))
		if err == nil {
			if err = conn.StartTLS(cfg); err != nil {
				conn.Close()
				return nil, fmt.Errorf("start tls: %w", err)
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	conn.SetTimeout(s.timeout())
	return conn, nil
}

func (Client) bindService(conn *ldap.Conn, s Settings) error {
	if s.BindDN == "" {
		return conn.UnauthenticatedBind("")
	}
	if err := conn.Bind(s.BindDN, s.BindPassword); err != nil {
		return fmt.Errorf("bind service account: %w", err)
	}
	return nil
}

func nonEmpty(values ...string) []string {
	var out []string
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Directory attributes that mark a disabled or locked account (Active Directory, 389/FreeIPA, OpenLDAP ppolicy).
var disabledAttrs = []string{"userAccountControl", "nsAccountLock", "pwdAccountLockedTime"}

func isDisabled(e *ldap.Entry) bool {
	if uac := e.GetAttributeValue("userAccountControl"); uac != "" {
		if v, err := strconv.ParseInt(uac, 10, 64); err == nil && v&0x2 != 0 {
			return true
		}
	}
	if strings.EqualFold(e.GetAttributeValue("nsAccountLock"), "true") {
		return true
	}
	return e.GetAttributeValue("pwdAccountLockedTime") != ""
}

func (c Client) findUser(conn *ldap.Conn, s Settings, username string) (*ldap.Entry, error) {
	filter := strings.ReplaceAll(s.UserFilter, "%s", ldap.EscapeFilter(username))
	attrs := append(nonEmpty(s.DisplayNameAttr, s.EmailAttr, s.PhoneAttr, s.MemberOfAttr), disabledAttrs...)
	req := ldap.NewSearchRequest(s.UserBaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 2, int(s.timeout().Seconds()), false, filter, attrs, nil)
	res, err := conn.Search(req)
	if err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultSizeLimitExceeded) {
			return nil, ErrAmbiguousUser
		}
		return nil, fmt.Errorf("search user: %w", err)
	}
	switch len(res.Entries) {
	case 0:
		return nil, ErrUserNotFound
	case 1:
		return res.Entries[0], nil
	}
	return nil, ErrAmbiguousUser
}

func (c Client) groups(conn *ldap.Conn, s Settings, e *ldap.Entry, username string) ([]string, error) {
	userDN := e.DN
	names := memberOfGroups(e, s.MemberOfAttr)
	if s.GroupBaseDN == "" || s.GroupFilter == "" {
		return names, nil
	}
	attr := s.GroupNameAttr
	if attr == "" {
		attr = "cn"
	}
	filter := strings.ReplaceAll(strings.ReplaceAll(s.GroupFilter, "%s", ldap.EscapeFilter(userDN)), "%u", ldap.EscapeFilter(username))
	req := ldap.NewSearchRequest(s.GroupBaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, int(s.timeout().Seconds()), false, filter, []string{attr}, nil)
	res, err := conn.Search(req)
	if err != nil {
		return nil, fmt.Errorf("search groups: %w", err)
	}
	for _, ge := range res.Entries {
		names = append(names, ge.GetAttributeValues(attr)...)
	}
	return names, nil
}

func (c Client) identity(conn *ldap.Conn, s Settings, e *ldap.Entry, username string) (*Identity, error) {
	groups, err := c.groups(conn, s, e, username)
	if err != nil {
		return nil, err
	}
	return &Identity{
		DN: e.DN, Username: username, DisplayName: e.GetAttributeValue(s.DisplayNameAttr), Email: e.GetAttributeValue(s.EmailAttr),
		Phone: e.GetAttributeValue(s.PhoneAttr), Groups: groups, Disabled: isDisabled(e),
	}, nil
}

func validLoginName(username string) bool {
	return username != "" && len(username) <= 255 && !strings.ContainsRune(username, 0)
}

// Authenticate implements Directory. An empty password is refused before any bind: many servers treat a bind
// with a DN and an empty password as an anonymous bind that succeeds.
func (c Client) Authenticate(ctx context.Context, s Settings, username, password string) (*Identity, error) {
	if !validLoginName(username) || password == "" {
		return nil, ErrInvalidCredentials
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := c.dial(s)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := c.bindService(conn, s); err != nil {
		return nil, err
	}
	entry, err := c.findUser(conn, s, username)
	if err != nil {
		return nil, err
	}
	if err := conn.Bind(entry.DN, password); err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("bind user: %w", err)
	}
	// read the groups as the service account again, so a user without read access to groups still resolves
	if err := c.bindService(conn, s); err != nil {
		return nil, err
	}
	return c.identity(conn, s, entry, username)
}

// Lookup implements Directory.
func (c Client) Lookup(ctx context.Context, s Settings, username string) (*Identity, error) {
	if !validLoginName(username) {
		return nil, ErrUserNotFound
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := c.dial(s)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := c.bindService(conn, s); err != nil {
		return nil, err
	}
	entry, err := c.findUser(conn, s, username)
	if err != nil {
		return nil, err
	}
	return c.identity(conn, s, entry, username)
}

// Check implements Directory.
func (c Client) Check(ctx context.Context, s Settings) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	conn, err := c.dial(s)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := c.bindService(conn, s); err != nil {
		return err
	}
	req := ldap.NewSearchRequest(s.UserBaseDN, ldap.ScopeBaseObject, ldap.NeverDerefAliases, 1, int(s.timeout().Seconds()), false, "(objectClass=*)", []string{"1.1"}, nil)
	if _, err := conn.Search(req); err != nil {
		return fmt.Errorf("search user base: %w", err)
	}
	return nil
}

// memberOfGroups reads the group DNs the user entry lists in attr (memberOf). Each group is returned twice, as its full DN and as the
// value of its first RDN (cn=ops,ou=groups,... gives "ops"), so a mapping may name either.
func memberOfGroups(e *ldap.Entry, attr string) []string {
	if attr == "" {
		return nil
	}
	var out []string
	for _, dn := range e.GetAttributeValues(attr) {
		out = append(out, dn)
		if parsed, err := ldap.ParseDN(dn); err == nil && len(parsed.RDNs) > 0 && len(parsed.RDNs[0].Attributes) > 0 {
			out = append(out, parsed.RDNs[0].Attributes[0].Value)
		}
	}
	return out
}
