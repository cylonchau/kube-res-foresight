package api

import (
	"bufio"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// minimalSMTP accepts one mail at a time and keeps the recipient lines; refuse makes it answer RCPT with a refusal.
func minimalSMTP(t *testing.T, refuse bool) (port int, rcpts func() []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	var mu sync.Mutex
	var got []string
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				say := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
				say("220 test")
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					cmd := strings.ToUpper(strings.TrimSpace(line))
					switch {
					case strings.HasPrefix(cmd, "RCPT"):
						if refuse {
							say("550 no such user")
							continue
						}
						mu.Lock()
						got = append(got, strings.TrimSpace(line))
						mu.Unlock()
						say("250 ok")
					case cmd == "DATA":
						say("354 go")
						for {
							l, err := r.ReadString('\n')
							if err != nil || l == ".\r\n" {
								break
							}
						}
						say("250 queued")
					case cmd == "QUIT":
						say("221 bye")
						return
					default:
						say("250 ok")
					}
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

func TestTheTestMailSaysWhetherTheSMTPSettingsWork(t *testing.T) {
	a := newAssistantRig(t, false)
	a.r.POST("/configs/notify-email/test", (&ConfigRouter{DAO: a.config}).TestEmail)
	send := func(to string) (int, string) {
		rec := performJSON(t, a.r, http.MethodPost, "/configs/notify-email/test", "", map[string]any{"to": to})
		return rec.Code, rec.Body.String()
	}
	// nothing is set up yet: the answer says what is missing
	code, body := send("ann@example.com")
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, body, "notify.email.host")

	port, rcpts := minimalSMTP(t, false)
	for k, v := range map[string]string{"host": "127.0.0.1", "port": strconv.Itoa(port), "security": "none", "from": "reports@example.com"} {
		require.NoError(t, model.DB.Create(&model.SysConfig{ConfigKey: "notify.email." + k, ConfigValue: v, ConfigName: k}).Error)
	}
	code, body = send("ann@example.com")
	require.Equal(t, http.StatusOK, code, body)
	assert.Contains(t, strings.Join(rcpts(), "|"), "ann@example.com")

	// an address that is not one is refused before anything is sent
	for _, bad := range []string{"", "not-an-address", "a@example.com\r\nBcc: b@example.com", "a@example.com,b@example.com"} {
		code, _ = send(bad)
		assert.Equal(t, http.StatusBadRequest, code, "%q", bad)
	}
	assert.Len(t, rcpts(), 1)
}

func TestTheTestMailReportsTheRefusalOfTheServer(t *testing.T) {
	a := newAssistantRig(t, false)
	a.r.POST("/configs/notify-email/test", (&ConfigRouter{DAO: a.config}).TestEmail)
	port, _ := minimalSMTP(t, true)
	for k, v := range map[string]string{"host": "127.0.0.1", "port": strconv.Itoa(port), "security": "none", "from": "reports@example.com"} {
		require.NoError(t, model.DB.Create(&model.SysConfig{ConfigKey: "notify.email." + k, ConfigValue: v, ConfigName: k}).Error)
	}
	rec := performJSON(t, a.r, http.MethodPost, "/configs/notify-email/test", "", map[string]any{"to": "nobody@example.com"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "refused the recipient")
}
