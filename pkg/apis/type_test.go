package apis

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestListOptionsOffset(t *testing.T) {
	assert.Equal(t, uint16(0), (&ListOptions{Page: 1, PageSize: 20}).Offset())
	assert.Equal(t, uint16(40), (&ListOptions{Page: 3, PageSize: 20}).Offset())
}
