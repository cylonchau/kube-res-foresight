package email

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/notify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type params map[string]string

func (p params) GetValueOrDefault(_ context.Context, key, fallback string) (string, error) {
	if v, ok := p[key]; ok {
		return v, nil
	}
	return fallback, nil
}

// fakeSMTP is a small SMTP server: it records the commands and the message, and can answer the sign-in with a refusal.
type fakeSMTP struct {
	ln       net.Listener
	useTLS   bool // the whole connection is TLS
	refuse   bool // refuse the sign-in
	mu       sync.Mutex
	commands []string
	data     string
}

func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func startSMTP(t *testing.T, useTLS bool) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	f := &fakeSMTP{ln: ln, useTLS: useTLS}
	cert := selfSigned(t)
	if useTLS {
		f.ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}})
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := f.ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, s)
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		f.record(line)
		switch cmd := strings.ToUpper(line); {
		case strings.HasPrefix(cmd, "EHLO"):
			say("250-fake\r\n250 AUTH PLAIN")
		case strings.HasPrefix(cmd, "AUTH"):
			if f.refuse {
				say("535 5.7.8 bad credentials")
			} else {
				say("235 ok")
			}
		case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
			say("250 ok")
		case cmd == "DATA":
			say("354 go on")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.data = b.String()
			f.mu.Unlock()
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func (f *fakeSMTP) sent() (string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data, append([]string(nil), f.commands...)
}

func base(f *fakeSMTP, security string) params {
	return params{KeyHost: "127.0.0.1", KeyPort: strconv.Itoa(f.port()), KeySecurity: security, KeyFrom: "reports@example.com", KeySkipVerify: "true"}
}

func body(t *testing.T, data string) string {
	t.Helper()
	_, b, ok := strings.Cut(data, "\r\n\r\n")
	require.True(t, ok, data)
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(b, "\r\n", ""))
	require.NoError(t, err)
	return string(raw)
}

func TestAMailIsDeliveredToAServerWithoutEncryptionAndTheContentIsNotInTheHeaders(t *testing.T) {
	f := startSMTP(t, false)
	cfg := base(f, "none")
	n := &Notifier{Config: cfg}
	err := n.Send(context.Background(), "ann@example.com", notify.Message{Title: "Weekly 用量\r\nBcc: evil@example.com", Content: "全部模块正常\nsecond line"})
	require.NoError(t, err)
	data, commands := f.sent()
	assert.Contains(t, strings.Join(commands, "|"), "RCPT TO:<ann@example.com>")
	assert.NotContains(t, data, "\r\nBcc:", "a line break in the title cannot start a header")
	assert.Contains(t, data, "To: ann@example.com")
	assert.Contains(t, data, "Subject: =?utf-8?")
	assert.Equal(t, "全部模块正常\nsecond line", body(t, data))
}

func TestTheWholeConnectionCanBeTLSAndTheSignInIsSentOverIt(t *testing.T) {
	f := startSMTP(t, true)
	cfg := base(f, "tls")
	cfg[KeyUsername] = "robot"
	require.NoError(t, (&Notifier{Config: cfg}).Send(context.Background(), "ann@example.com", notify.Message{Title: "t", Content: "c"}))
	_, commands := f.sent()
	assert.Contains(t, strings.Join(commands, "|"), "AUTH PLAIN")
}

func TestTheCertificateIsCheckedUnlessTheAdministratorSwitchesItOff(t *testing.T) {
	f := startSMTP(t, true)
	cfg := base(f, "tls")
	cfg[KeySkipVerify] = "false"
	err := (&Notifier{Config: cfg}).Send(context.Background(), "ann@example.com", notify.Message{Title: "t", Content: "c"})
	require.Error(t, err, "a self-signed certificate is refused")
	data, _ := f.sent()
	assert.Empty(t, data)
}

func TestARefusedSignInIsReportedAndNothingIsSent(t *testing.T) {
	f := startSMTP(t, true)
	f.refuse = true
	cfg := base(f, "tls")
	cfg[KeyUsername] = "robot"
	err := (&Notifier{Config: cfg}).Send(context.Background(), "ann@example.com", notify.Message{Title: "t", Content: "c"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sign-in")
	data, _ := f.sent()
	assert.Empty(t, data)
}

func TestThePasswordIsNotSentInTheClearToAServerOnTheNetwork(t *testing.T) {
	// the client library refuses to sign in over a connection that is not encrypted, unless the server is on this machine; a name that is
	// not local is refused before the password leaves
	f := startSMTP(t, false)
	cfg := base(f, "none")
	cfg[KeyUsername] = "robot"
	cfg[KeyPassword] = ""
	n := &Notifier{Config: cfg, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:"+strconv.Itoa(f.port()))
	}}
	cfg[KeyHost] = "smtp.remote.example"
	err := n.Send(context.Background(), "ann@example.com", notify.Message{Title: "t", Content: "c"})
	require.Error(t, err)
	_, commands := f.sent()
	assert.NotContains(t, strings.Join(commands, "|"), "AUTH")
}

func TestTheChannelSaysWhatIsMissingAndRefusesWhatIsNotAnAddress(t *testing.T) {
	n := &Notifier{Config: params{}}
	err := n.Send(context.Background(), "ann@example.com", notify.Message{Title: "t", Content: "c"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "notify.email.host")

	n = &Notifier{Config: params{KeyHost: "h", KeyFrom: "reports@example.com"}}
	for _, bad := range []string{"", "no-at", "a@b", "x@example.com\r\nBcc: y@example.com", "Ann <ann@example.com>", "a@example.com,b@example.com", "\"a b\"@example.com"} {
		assert.Error(t, n.Send(context.Background(), bad, notify.Message{}), "%q", bad)
		_, err := ValidAddress(bad)
		assert.Error(t, err, "%q", bad)
	}
	got, err := ValidAddress(" ann@example.com ")
	require.NoError(t, err)
	assert.Equal(t, "ann@example.com", got)
}
