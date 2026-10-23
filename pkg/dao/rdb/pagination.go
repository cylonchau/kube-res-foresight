package rdb

import "strings"

func pageBounds(page, pageSize int) (int, int) { return normalizePage(page, pageSize) }
func like(value string) string                 { return "%" + strings.TrimSpace(value) + "%" }
func trimTo(value string, n int) string {
	if len(value) <= n {
		return value
	}
	return value[:n]
}

// normalizePage clamps page/pageSize to the convention shared by every
// admin ListPaginated method in this package: page defaults to 1,
// pageSize defaults to 20 and is capped at 200.
func normalizePage(page, pageSize int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 20
	}
	return page, pageSize
}

// pageOffset computes the SQL OFFSET for an already-normalized page/pageSize.
func pageOffset(page, pageSize int) int {
	return (page - 1) * pageSize
}
