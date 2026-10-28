package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAJSONBodyThatIsNotUTF8IsRefusedAndOneThatIsPassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(UTF8BodyMiddleware())
	var seen string
	engine.POST("/x", func(c *gin.Context) {
		raw := make([]byte, 256)
		n, _ := c.Request.Body.Read(raw)
		seen = string(raw[:n])
		c.String(http.StatusOK, "ok")
	})

	good := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"remark":"数据库中的 operator"}`))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(good, req)
	require.Equal(t, http.StatusOK, good.Code)
	assert.Equal(t, `{"remark":"数据库中的 operator"}`, seen, "a valid body reaches the handler whole")

	// "数据库" in GBK
	gbk := "{\"remark\":\"" + string([]byte{0xca, 0xfd, 0xbe, 0xdd, 0xbf, 0xe2}) + "\"}"
	bad := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(gbk))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(bad, req)
	assert.Equal(t, http.StatusBadRequest, bad.Code)
	assert.Contains(t, bad.Body.String(), "UTF-8")

	// a read is never inspected
	get := httptest.NewRecorder()
	engine.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/x", nil))
	assert.Equal(t, http.StatusNotFound, get.Code)
}

func TestTruncateCutsAtACharacterBoundary(t *testing.T) {
	// 4 Chinese characters are 12 bytes; cutting at 7 would leave half of the third one
	cut := truncate("数据库中", 7)
	assert.Equal(t, "数据", cut)
	assert.True(t, strings.ToValidUTF8(cut, "?") == cut)
	assert.Equal(t, "abc", truncate("abcdef", 3))
}
