package app

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pageFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":        {Data: []byte("<html>" + strings.Repeat("x", 3000) + "</html>")},
		"assets/app-abc.js": {Data: []byte(strings.Repeat("const a = 1;\n", 500))},
		"logo.png":          {Data: bytes.Repeat([]byte{1, 2, 3}, 2000)},
	}
}

// the scripts go out compressed when the browser takes it, with a cache rule by the kind of file, and a file that has not changed costs a 304
func TestTheFilesOfThePageAreServedCompressedAndCached(t *testing.T) {
	fsys := pageFS()
	js := serveStatic(fsys, "assets/app-abc.js", "gzip, deflate, br", "")
	require.Equal(t, http.StatusOK, js.Status)
	assert.Equal(t, "gzip", js.Header["Content-Encoding"])
	assert.Equal(t, "public, max-age=31536000, immutable", js.Header["Cache-Control"], "a file named by its content is kept")
	zr, err := gzip.NewReader(bytes.NewReader(js.Body))
	require.NoError(t, err)
	plain, err := io.ReadAll(zr)
	require.NoError(t, err)
	assert.Equal(t, fsys["assets/app-abc.js"].Data, plain)
	assert.Less(t, len(js.Body), len(plain)/5)

	raw := serveStatic(fsys, "assets/app-abc.js", "", "")
	assert.Empty(t, raw.Header["Content-Encoding"], "a browser that does not take gzip gets the file as it is")
	assert.Equal(t, fsys["assets/app-abc.js"].Data, raw.Body)

	page := serveStatic(fsys, "index.html", "gzip", "")
	assert.Equal(t, "no-cache", page.Header["Cache-Control"], "the page is checked again each time")
	assert.Equal(t, "text/html; charset=utf-8", page.Header["Content-Type"])

	again := serveStatic(fsys, "index.html", "gzip", page.Header["ETag"])
	assert.Equal(t, http.StatusNotModified, again.Status)
	assert.Empty(t, again.Body)

	img := serveStatic(fsys, "logo.png", "gzip", "")
	assert.Empty(t, img.Header["Content-Encoding"], "an image is already compressed")
	assert.True(t, serveStatic(fsys, "missing.js", "gzip", "").Missing)
}
