package model

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"sync"
	"testing"
)

func TestMessageTextRoundTripsThroughCompression(t *testing.T) {
	for name, text := range map[string]string{
		"short":   "BTC up",
		"unicode": "比特币突破 7 万美元 🚀 — 多头情绪升温",
		"long":    strings.Repeat("BTC breaks out of the range with strong volume. ", 80),
		"binary":  "\x00\x01\x02 mixed \xff bytes",
	} {
		body := CompressText(text)
		got, err := DecompressText(body)
		require.NoError(t, err, name)
		assert.Equal(t, text, got, name)
	}
	assert.Nil(t, CompressText(""), "an empty text stores nothing")
	got, err := DecompressText(nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestCompressionSavesStorageAndNeverCostsMoreThanOneByte(t *testing.T) {
	long := strings.Repeat("The market is moving fast today, watch the order book. ", 60)
	assert.Less(t, len(CompressText(long)), len(long)/5, "repetitive text shrinks a lot")

	// A text too short to gain anything is kept raw: one flag byte of overhead at most.
	short := "hi"
	body := CompressText(short)
	assert.Equal(t, bodyRaw, body[0])
	assert.Equal(t, len(short)+1, len(body))

	// Incompressible data falls back to raw as well.
	noise := make([]byte, 400)
	for i := range noise {
		noise[i] = byte((i*131 + i*i*7) % 251)
	}
	assert.LessOrEqual(t, len(CompressText(string(noise))), len(noise)+1)
}

func TestUnreadableBodiesAreReported(t *testing.T) {
	_, err := DecompressText([]byte{9, 'x'})
	assert.ErrorIs(t, err, ErrBadBody, "an unknown encoding")
	_, err = DecompressText([]byte{bodyZstd, 1, 2, 3})
	assert.ErrorIs(t, err, ErrBadBody, "a corrupt zstd frame")
	m := TelegramClientMessage{Body: []byte{9}}
	assert.ErrorIs(t, m.LoadText(), ErrBadBody)
}

func TestSetTextRecordsTheOriginalSizeAndLoadTextRestoresIt(t *testing.T) {
	var m TelegramClientMessage
	m.SetText("hello world")
	assert.EqualValues(t, 11, m.Size)
	m.Text = ""
	require.NoError(t, m.LoadText())
	assert.Equal(t, "hello world", m.Text)

	m.SetText(strings.Repeat("x", 70000))
	assert.EqualValues(t, 65535, m.Size, "the size column is capped")
	require.NoError(t, m.LoadText())
	assert.Len(t, m.Text, 70000)
}

func TestCompressionIsSafeForConcurrentUse(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			text := strings.Repeat("message ", 20+i)
			got, err := DecompressText(CompressText(text))
			assert.NoError(t, err)
			assert.Equal(t, text, got)
		}(i)
	}
	wg.Wait()
}

func TestForwardClassesSeparateTasksFromUsers(t *testing.T) {
	assert.Equal(t, ForwardClassUser, ForwardClassOf(ForwardTargetNotify))
	for _, kind := range []string{ForwardTargetChannel, ForwardTargetJob, ForwardTargetRedis} {
		assert.Equal(t, ForwardClassTask, ForwardClassOf(kind), kind)
	}
	assert.Equal(t, []string{ForwardTargetNotify}, ForwardKindsOf(ForwardClassUser))
	assert.Equal(t, []string{ForwardTargetChannel}, ForwardKindsOf(ForwardClassTask), "a task rule writes to a channel")
	assert.Equal(t, []string{ForwardTargetChannel}, ForwardKindsOf(""), "an unknown class means the task kinds")

	rule := &TelegramForwardRule{TargetKind: ForwardTargetKafka}
	require.NoError(t, rule.AfterFind(nil))
	assert.Equal(t, ForwardClassTask, rule.TargetClass)
}
