package model

import (
	"errors"
	"github.com/klauspost/compress/zstd"
	"sync"
)

// Cached Telegram message text is stored compressed: storage cost comes first,
// CPU and memory second. The first byte says how the rest is encoded, so a
// message too short to gain from compression is kept as it is.
const (
	bodyRaw  byte = 0
	bodyZstd byte = 1
)

// ErrBadBody means a stored body cannot be decoded.
var ErrBadBody = errors.New("unreadable message body")

var (
	zstdOnce    sync.Once
	zstdEncoder *zstd.Encoder
	zstdDecoder *zstd.Decoder
)

// zstdCodec builds the shared encoder and decoder once. The encoder uses the
// strongest level and drops the checksum and a large window, because the texts
// are small and the stored size is what matters.
func zstdCodec() (*zstd.Encoder, *zstd.Decoder) {
	zstdOnce.Do(func() {
		zstdEncoder, _ = zstd.NewWriter(nil,
			zstd.WithEncoderLevel(zstd.SpeedBestCompression),
			zstd.WithEncoderCRC(false),
			zstd.WithEncoderConcurrency(1),
			zstd.WithWindowSize(32<<10),
			zstd.WithZeroFrames(false),
		)
		zstdDecoder, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(8<<20))
	})
	return zstdEncoder, zstdDecoder
}

// CompressText encodes text for storage.
func CompressText(text string) []byte {
	if text == "" {
		return nil
	}
	enc, _ := zstdCodec()
	packed := enc.EncodeAll([]byte(text), []byte{bodyZstd})
	if len(packed) < len(text)+1 {
		return packed
	}
	return append([]byte{bodyRaw}, text...)
}

// DecompressText is the inverse of CompressText.
func DecompressText(body []byte) (string, error) {
	if len(body) == 0 {
		return "", nil
	}
	switch body[0] {
	case bodyRaw:
		return string(body[1:]), nil
	case bodyZstd:
		_, dec := zstdCodec()
		out, err := dec.DecodeAll(body[1:], nil)
		if err != nil {
			return "", ErrBadBody
		}
		return string(out), nil
	}
	return "", ErrBadBody
}

// SetText stores text compressed and records its original size.
func (m *TelegramClientMessage) SetText(text string) {
	m.Text = text
	m.Body = CompressText(text)
	size := len(text)
	if size > 65535 {
		size = 65535
	}
	m.Size = uint16(size)
}

// LoadText fills Text from the stored body.
func (m *TelegramClientMessage) LoadText() error {
	text, err := DecompressText(m.Body)
	if err != nil {
		return err
	}
	m.Text = text
	return nil
}
