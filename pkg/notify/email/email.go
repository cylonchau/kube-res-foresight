// Package email is the e-mail notification channel: it delivers a message to one address through an SMTP server named in the system
// parameters (notify.email.*). It is a channel like the webhook one, with no knowledge of what is sent: the AI scheduled tasks use it to mail
// their reports, and any producer of notifications may.
package email

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/notify"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	cryptoutils "github.com/cylonchau/kube-res-foresight/pkg/utils"
)

// Channel is the identifier of the channel.
const Channel = "email"

// The system parameters of the channel. The password is a secret: stored encrypted, shown as a mask, kept when the mask is sent back.
const (
	KeyHost     = "notify.email.host"
	KeyPort     = "notify.email.port"
	KeyUsername = "notify.email.username"
	KeyPassword = "notify.email.password"
	KeyFrom     = "notify.email.from"
	// KeySecurity is "starttls" (the usual port 587), "tls" (the whole connection is TLS, usually port 465) or "none".
	KeySecurity   = "notify.email.security"
	KeySkipVerify = "notify.email.skip_verify"
)

// KeyPrefix is the prefix of the parameters of the channel.
const KeyPrefix = "notify.email."

// Secret reports whether the parameter holds a credential.
func Secret(key string) bool { return key == KeyPassword }

// DefaultConfigs are the parameters the platform starts with: nothing is set, so the channel is not usable until an administrator fills it in.
func DefaultConfigs() []model.SysConfig {
	const remark = "E-mail channel (SMTP) of the notifications, used for example by the AI scheduled tasks to mail their reports."
	return []model.SysConfig{
		{ConfigKey: KeyHost, ConfigValue: "", ConfigName: "E-mail SMTP Host", Remark: remark + " The host of the SMTP server."},
		{ConfigKey: KeyPort, ConfigValue: "587", ConfigName: "E-mail SMTP Port", Remark: "The port of the SMTP server: 587 with starttls, 465 with tls, 25 with none."},
		{ConfigKey: KeySecurity, ConfigValue: "starttls", ConfigName: "E-mail Security", Remark: "starttls, tls or none. A user name and password are sent only over starttls or tls (or to a server on this machine)."},
		{ConfigKey: KeyUsername, ConfigValue: "", ConfigName: "E-mail SMTP User", Remark: "The account that signs in to the SMTP server; empty when the server needs none."},
		{ConfigKey: KeyPassword, ConfigValue: "", ConfigName: "E-mail SMTP Password", Remark: "The password of the account. Stored encrypted and never shown."},
		{ConfigKey: KeyFrom, ConfigValue: "", ConfigName: "E-mail Sender", Remark: "The address the mails come from, for example reports@example.com or \"kube-res-foresight <reports@example.com>\"."},
		{ConfigKey: KeySkipVerify, ConfigValue: "false", ConfigName: "E-mail Skip Certificate Check", Remark: "true does not check the certificate of the SMTP server (a server with a self-signed certificate). The connection can then be impersonated."},
	}
}

// Settings is the SMTP configuration, read from the system parameters when a message is sent.
type Settings struct {
	Host, Username, Password, Security string
	Port                               int
	From                               *mail.Address
	SkipVerify                         bool
	Timeout                            time.Duration
}

// ConfigReader is the part of the system parameters the channel reads.
type ConfigReader interface {
	GetValueOrDefault(ctx context.Context, key, fallback string) (string, error)
}

// Load reads the settings.
func Load(ctx context.Context, r ConfigReader) (Settings, error) {
	get := func(key, fallback string) string {
		v, _ := r.GetValueOrDefault(ctx, key, fallback)
		return strings.TrimSpace(v)
	}
	s := Settings{Host: get(KeyHost, ""), Username: get(KeyUsername, ""), Security: strings.ToLower(get(KeySecurity, "starttls"))}
	s.Port, _ = strconv.Atoi(get(KeyPort, "587"))
	s.SkipVerify = strings.EqualFold(get(KeySkipVerify, "false"), "true")
	s.Timeout = timing.Seconds(timing.NotifyEmailTimeout.Parse(get(timing.NotifyEmailTimeout.Key, "")))
	if stored := get(KeyPassword, ""); stored != "" {
		if plain, err := cryptoutils.DecryptValue(stored); err == nil {
			s.Password = plain
		}
	}
	if from := get(KeyFrom, ""); from != "" {
		addr, err := mail.ParseAddress(from)
		if err != nil {
			return s, fmt.Errorf("the sender address %q is not valid", from)
		}
		s.From = addr
	}
	return s, nil
}

// Problem says what is missing, or "".
func (s Settings) Problem() string {
	switch {
	case s.Host == "":
		return "the e-mail channel is not set up: the SMTP host (notify.email.host) is empty"
	case s.Port < 1 || s.Port > 65535:
		return "the SMTP port (notify.email.port) is not valid"
	case s.From == nil:
		return "the sender address (notify.email.from) is not set"
	case s.Security != "starttls" && s.Security != "tls" && s.Security != "none":
		return "the security (notify.email.security) is starttls, tls or none"
	}
	return ""
}

// Notifier is the e-mail channel.
type Notifier struct {
	Config ConfigReader
	// Dial opens the connection to the server; nil dials the network. Tests give their own.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// New is the channel reading its settings from the system parameters.
func New(cfg *rdb.SysConfigDAO) *Notifier { return &Notifier{Config: cfg} }

func (n *Notifier) Channel() string { return Channel }

// Recipients is empty: whom to mail is the producer's choice (a task names its recipients).
func (n *Notifier) Recipients(context.Context) ([]string, error) { return []string{}, nil }

// ValidAddress checks a bare e-mail address (no display name, no line break) and returns it.
func ValidAddress(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\r\n<>\",;") {
		return "", fmt.Errorf("%q is not an e-mail address", raw)
	}
	addr, err := mail.ParseAddress(raw)
	if err != nil || addr.Address != raw || !strings.Contains(raw[strings.LastIndex(raw, "@"):], ".") {
		return "", fmt.Errorf("%q is not an e-mail address", raw)
	}
	return raw, nil
}

func header(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }

// message builds the mail: UTF-8 text in base64, the subject encoded, no header taken from the content.
func message(from *mail.Address, to string, msg notify.Message) []byte {
	var id [12]byte
	_, _ = rand.Read(id[:])
	body := base64.StdEncoding.EncodeToString([]byte(msg.Content))
	var wrapped strings.Builder
	for len(body) > 76 {
		wrapped.WriteString(body[:76] + "\r\n")
		body = body[76:]
	}
	wrapped.WriteString(body + "\r\n")
	domain := "localhost"
	if at := strings.LastIndex(from.Address, "@"); at >= 0 {
		domain = from.Address[at+1:]
	}
	headers := []string{
		"From: " + header(from.String()),
		"To: " + header(to),
		"Subject: " + mime.QEncoding.Encode("utf-8", header(msg.Title)),
		"Date: " + time.Now().Format(time.RFC1123Z),
		"Message-ID: <" + hex.EncodeToString(id[:]) + "@" + header(domain) + ">",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: base64",
	}
	return []byte(strings.Join(headers, "\r\n") + "\r\n\r\n" + wrapped.String())
}

// Send delivers msg to one address.
func (n *Notifier) Send(ctx context.Context, recipient string, msg notify.Message) error {
	to, err := ValidAddress(recipient)
	if err != nil {
		return err
	}
	s, err := Load(ctx, n.Config)
	if err != nil {
		return err
	}
	if p := s.Problem(); p != "" {
		return errors.New(p)
	}
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()

	address := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	dial := n.Dial
	if dial == nil {
		dialer := &net.Dialer{}
		dial = dialer.DialContext
	}
	conn, err := dial(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("could not reach the SMTP server %s: %w", address, err)
	}
	// the deadline of the whole exchange is the context's
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	tlsConfig := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: s.SkipVerify} //nolint:gosec // the administrator asked for it
	if s.Security == "tls" {
		conn = tls.Client(conn, tlsConfig)
	}
	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("the SMTP server did not greet: %w", err)
	}
	defer client.Close()
	if s.Security == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("the SMTP server does not offer STARTTLS: set notify.email.security to none, or use a server that does")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("STARTTLS failed: %w", err)
		}
	}
	if s.Username != "" {
		// smtp.PlainAuth refuses to send the password over a connection that is not encrypted, unless the server is on this machine
		if err := client.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return fmt.Errorf("the SMTP server refused the sign-in: %w", err)
		}
	}
	if err := client.Mail(s.From.Address); err != nil {
		return fmt.Errorf("the SMTP server refused the sender: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("the SMTP server refused the recipient %s: %w", to, err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("the SMTP server did not accept the message: %w", err)
	}
	if _, err := w.Write(message(s.From, to, msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("the SMTP server did not accept the message: %w", err)
	}
	return client.Quit()
}
