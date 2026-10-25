package api

import (
	"github.com/gin-gonic/gin"
	"strconv"
	"strings"
	"time"
)

// boolQuery reads an optional true/false filter; absent or anything else means "any".
func boolQuery(c *gin.Context, key string) *bool {
	switch c.Query(key) {
	case "true":
		value := true
		return &value
	case "false":
		value := false
		return &value
	}
	return nil
}

// timeQuery reads an optional RFC3339 time filter.
func timeQuery(c *gin.Context, key string) *time.Time {
	value := strings.TrimSpace(c.Query(key))
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	utc := parsed.UTC() // rows are stored in UTC: a bound in another zone would compare as text against another offset
	return &utc
}

// pageParams reads page and page_size with the project defaults.
func pageParams(c *gin.Context) (page, pageSize int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ = strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}
	return page, pageSize
}

// paginate slices an already-computed list (e.g. live status merged from several
// tables) and reports the unsliced total.
func paginate[T any](items []T, page, pageSize int) ([]T, int) {
	total := len(items)
	start := (page - 1) * pageSize
	if start >= total {
		return []T{}, total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return items[start:end], total
}
