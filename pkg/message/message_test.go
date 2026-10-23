package message

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestSinkFuncDeliversEvents(t *testing.T) {
	var got Event
	var sink Sink = SinkFunc(func(e Event) { got = e })
	sink.Handle(Event{Type: EventChannelMessage, Message: Message{Platform: "telegram", Text: "hi"}})
	assert.Equal(t, EventChannelMessage, got.Type)
	assert.Equal(t, "hi", got.Message.Text)
}
