package telegramclient

import (
	"errors"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestPeerFloodIsRecognisedAndOtherErrorsAreLeftAlone(t *testing.T) {
	err := sendError(tgerr.New(400, "PEER_FLOOD"))
	assert.ErrorIs(t, err, ErrPeerFlood)
	assert.ErrorContains(t, err, "PEER_FLOOD")

	other := tgerr.New(400, "USER_PRIVACY_RESTRICTED")
	assert.Equal(t, error(other), sendError(other))
	assert.NotErrorIs(t, sendError(other), ErrPeerFlood)
	plain := errors.New("network")
	assert.Equal(t, plain, sendError(plain))
	assert.NoError(t, sendError(nil))
}
