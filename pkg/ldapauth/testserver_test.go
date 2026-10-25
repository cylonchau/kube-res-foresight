package ldapauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	goldap "github.com/lor00x/goldap/message"
	"github.com/stretchr/testify/require"
	ldapserver "github.com/vjeantet/ldapserver"
)

// entry is one object of the in-memory directory.
type entry struct {
	dn    string
	attrs map[string][]string
}

// testDirectory is a small LDAP server over real TLS sockets, so the go-ldap client code is exercised end to
// end: dial, TLS verification, bind, search and filter escaping.
type testDirectory struct {
	addr      string
	caPEM     string
	entries   []entry
	passwords map[string]string

	mu    sync.Mutex
	binds []string // DNs that were bound with a non-empty password
}

func newTestDirectory(t *testing.T) *testDirectory {
	t.Helper()
	d := &testDirectory{passwords: map[string]string{
		"cn=svc,dc=example,dc=org":               "svc-pw",
		"uid=alice,ou=people,dc=example,dc=org":  "alice-pw",
		"uid=bob,ou=people,dc=example,dc=org":    "bob-pw",
		"uid=dup1,ou=people,dc=example,dc=org":   "x",
		"uid=dup2,ou=people,dc=example,dc=org":   "x",
		"uid=ad-off,ou=people,dc=example,dc=org": "x",
	}}
	person := func(uid, cn, mail string, extra map[string][]string) entry {
		attrs := map[string][]string{"objectClass": {"person"}, "uid": {uid}, "cn": {cn}, "mail": {mail}}
		for k, v := range extra {
			attrs[k] = v
		}
		return entry{dn: fmt.Sprintf("uid=%s,ou=people,dc=example,dc=org", uid), attrs: attrs}
	}
	group := func(name string, members ...string) entry {
		return entry{dn: fmt.Sprintf("cn=%s,ou=groups,dc=example,dc=org", name), attrs: map[string][]string{
			"objectClass": {"groupOfNames"}, "cn": {name}, "member": members}}
	}
	alice, bob := "uid=alice,ou=people,dc=example,dc=org", "uid=bob,ou=people,dc=example,dc=org"
	d.entries = []entry{
		person("alice", "Alice A", "alice@example.org", map[string][]string{"telephoneNumber": {"+1 555 0100"}}),
		person("bob", "Bob B", "bob@example.org", map[string][]string{"nsAccountLock": {"true"}}),
		person("dup1", "Dup One", "d1@example.org", map[string][]string{"uid": {"dup", "dup1"}}),
		person("dup2", "Dup Two", "d2@example.org", map[string][]string{"uid": {"dup", "dup2"}}),
		person("ad-off", "AD Off", "off@example.org", map[string][]string{"userAccountControl": {"514"}}),
		group("sre", alice),
		group("dev", alice, bob),
	}

	cert, key, ca := selfSigned(t)
	d.caPEM = ca
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	_ = key

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	d.addr = probe.Addr().String()
	require.NoError(t, probe.Close())

	ldapserver.Logger = ldapserver.DiscardingLogger
	server := ldapserver.NewServer()
	routes := ldapserver.NewRouteMux()
	routes.Bind(d.handleBind)
	routes.Search(d.handleSearch)
	server.Handle(routes)
	go func() {
		_ = server.ListenAndServe(d.addr, func(s *ldapserver.Server) { s.Listener = tls.NewListener(s.Listener, tlsConfig) })
	}()
	require.Eventually(t, func() bool {
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", d.addr, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 5*time.Second, 20*time.Millisecond)
	return d
}

func selfSigned(t *testing.T) (tls.Certificate, *ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ldap.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, key, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func (d *testDirectory) handleBind(w ldapserver.ResponseWriter, m *ldapserver.Message) {
	r := m.GetBindRequest()
	res := ldapserver.NewBindResponse(ldapserver.LDAPResultSuccess)
	name, pass := string(r.Name()), string(r.AuthenticationSimple())
	if name == "" && pass == "" { // anonymous
		w.Write(res)
		return
	}
	d.mu.Lock()
	d.binds = append(d.binds, name+"|"+pass)
	d.mu.Unlock()
	if want, ok := d.passwords[name]; !ok || want != pass || pass == "" {
		res.SetResultCode(ldapserver.LDAPResultInvalidCredentials)
		res.SetDiagnosticMessage("invalid credentials")
	}
	w.Write(res)
}

func (d *testDirectory) handleSearch(w ldapserver.ResponseWriter, m *ldapserver.Message) {
	r := m.GetSearchRequest()
	base := strings.ToLower(string(r.BaseObject()))
	matched := 0
	for _, e := range d.entries {
		inScope := strings.HasSuffix(strings.ToLower(e.dn), base)
		if r.Scope() == ldapserver.SearchRequestScopeBaseObject {
			inScope = strings.ToLower(e.dn) == base
		}
		if !inScope {
			continue
		}
		if !matchFilter(r.Filter(), e) {
			continue
		}
		matched++
		if sizeLimit := int(r.SizeLimit()); sizeLimit > 0 && matched > sizeLimit {
			w.Write(ldapserver.NewSearchResultDoneResponse(ldapserver.LDAPResultSizeLimitExceeded))
			return
		}
		out := ldapserver.NewSearchResultEntry(e.dn)
		for k, vs := range e.attrs {
			for _, v := range vs {
				out.AddAttribute(mustAttr(k), mustValue(v))
			}
		}
		w.Write(out)
	}
	w.Write(ldapserver.NewSearchResultDoneResponse(ldapserver.LDAPResultSuccess))
}

func mustAttr(k string) goldap.AttributeDescription { return goldap.AttributeDescription(k) }
func mustValue(v string) goldap.AttributeValue      { return goldap.AttributeValue(v) }

func (d *testDirectory) boundWith(dn string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, b := range d.binds {
		if strings.HasPrefix(b, dn+"|") {
			return true
		}
	}
	return false
}

// matchFilter evaluates the decoded filter tree, so filter escaping done by the client is already undone: an
// assertion value is a literal, exactly as a real server treats it.
func matchFilter(f goldap.Filter, e entry) bool {
	switch v := f.(type) {
	case goldap.FilterAnd:
		for _, sub := range v {
			if !matchFilter(sub, e) {
				return false
			}
		}
		return true
	case goldap.FilterOr:
		for _, sub := range v {
			if matchFilter(sub, e) {
				return true
			}
		}
		return false
	case goldap.FilterEqualityMatch:
		a := goldap.AttributeValueAssertion(v)
		want := string(a.AssertionValue())
		for _, have := range e.attrs[string(a.AttributeDesc())] {
			if strings.EqualFold(have, want) {
				return true
			}
		}
		return false
	case goldap.FilterPresent:
		return len(e.attrs[string(v)]) > 0
	}
	return false
}
