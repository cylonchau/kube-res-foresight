package app

import (
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
)

// The files of the web page are embedded in the binary and never change while it runs, so each is read once, compressed once and kept: a page of 3 MB of scripts goes out as
// a few hundred KB, and a browser that has a file (the names of the files under assets/ carry a hash of their content) does not ask for it again.

// staticFile is one embedded file as it is served: the bytes, the gzip form of them (nil when it does not pay), the type and the tag of the content.
type staticFile struct {
	data        []byte
	gz          []byte
	contentType string
	etag        string
}

// staticAnswer is what a request for a file gets.
type staticAnswer struct {
	Status  int
	Header  map[string]string
	Body    []byte
	Missing bool // no such file: the caller falls back to the page of the single-page application
}

var (
	staticMu    sync.RWMutex
	staticCache = map[string]*staticFile{}
)

// compressible says whether a type shrinks enough to be sent compressed (images and fonts are already compressed).
func compressible(contentType string) bool {
	for _, prefix := range []string{"text/", "application/javascript", "application/json", "image/svg+xml", "application/xml"} {
		if strings.HasPrefix(contentType, prefix) {
			return true
		}
	}
	return false
}

func contentTypeOf(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "application/javascript"
	case ".css":
		return "text/css"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	}
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}

// loadStatic reads a file of the page once and keeps it with its compressed form.
func loadStatic(fsys fs.FS, name string) (*staticFile, error) {
	staticMu.RLock()
	f, ok := staticCache[name]
	staticMu.RUnlock()
	if ok {
		return f, nil
	}
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, err
	}
	sum := sha1.Sum(data)
	f = &staticFile{data: data, contentType: contentTypeOf(name), etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
	if compressible(f.contentType) && len(data) > 1024 {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		_, _ = zw.Write(data)
		if zw.Close() == nil && buf.Len() < len(data) {
			f.gz = buf.Bytes()
		}
	}
	staticMu.Lock()
	staticCache[name] = f
	staticMu.Unlock()
	return f, nil
}

// serveStatic answers a request for a file of the page: the file with its type, a cache rule (what is under assets/ is named by its content and is kept for a year; the page and
// the rest are checked again each time, with a tag so an unchanged one costs a 304), and the gzip form when the browser takes it.
func serveStatic(fsys fs.FS, name, acceptEncoding, ifNoneMatch string) staticAnswer {
	f, err := loadStatic(fsys, name)
	if err != nil {
		return staticAnswer{Missing: true}
	}
	h := map[string]string{"Content-Type": f.contentType, "ETag": f.etag, "Vary": "Accept-Encoding"}
	if strings.HasPrefix(name, "assets/") {
		h["Cache-Control"] = "public, max-age=31536000, immutable"
	} else {
		h["Cache-Control"] = "no-cache"
	}
	if ifNoneMatch != "" && strings.Contains(ifNoneMatch, f.etag) {
		return staticAnswer{Status: http.StatusNotModified, Header: h}
	}
	body := f.data
	if f.gz != nil && strings.Contains(acceptEncoding, "gzip") {
		body = f.gz
		h["Content-Encoding"] = "gzip"
	}
	return staticAnswer{Status: http.StatusOK, Header: h, Body: body}
}
