package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Frame layout per WIRE.md §2:
//
//	[flags:8][channel-id:24][length:32][payload:length]
//
// All multi-byte integers are big-endian.
const (
	// FlagEnd marks the last (or only) fragment of a logical message.
	// v0.0 senders MUST set this; fragmentation is reserved.
	FlagEnd byte = 0x01

	// flagReserved is the mask of bits that MUST be zero in v0.0.
	flagReserved byte = 0xFE

	// MaxPayload is the per-frame payload cap (16 MiB) from WIRE.md §2.
	MaxPayload = 16 * 1024 * 1024

	// MaxChannel is the largest legal channel id (24-bit space).
	MaxChannel uint32 = (1 << 24) - 1

	// headerSize is the size of the fixed frame header in bytes.
	headerSize = 8
)

// Errors returned by Frame encoding/decoding.
var (
	ErrOversizeFrame  = errors.New("wire: frame length exceeds 16 MiB cap")
	ErrChannelTooLarge = errors.New("wire: channel id exceeds 24-bit space")
	ErrReservedFlag   = errors.New("wire: reserved flag bit set")
	ErrEndNotSet      = errors.New("wire: END flag MUST be set in v0.0")
	ErrShortHeader    = errors.New("wire: short header")
	ErrShortPayload   = errors.New("wire: short payload")
)

// Frame is a decoded wash frame. Payload aliases the buffer the caller
// supplies; callers that retain frames across reads must copy it.
type Frame struct {
	Flags   byte
	Channel uint32
	Payload []byte
}

// End reports whether the END flag is set on f.
func (f Frame) End() bool { return f.Flags&FlagEnd != 0 }

// Validate checks the v0.0 wire invariants. See WIRE.md §2.
//
// In v0.0 every send MUST set END=1, reserved flag bits MUST be zero,
// channel id MUST fit in 24 bits, and payload length MUST be ≤ 16 MiB.
func (f Frame) Validate() error {
	if f.Channel > MaxChannel {
		return ErrChannelTooLarge
	}
	if f.Flags&flagReserved != 0 {
		return ErrReservedFlag
	}
	if !f.End() {
		return ErrEndNotSet
	}
	if len(f.Payload) > MaxPayload {
		return ErrOversizeFrame
	}
	return nil
}

// EncodeFrame writes f to w. It validates f first; an invalid frame
// is never partially written.
func EncodeFrame(w io.Writer, f Frame) error {
	if err := f.Validate(); err != nil {
		return err
	}
	var hdr [headerSize]byte
	hdr[0] = f.Flags
	// big-endian 24-bit channel in hdr[1..4]
	hdr[1] = byte(f.Channel >> 16)
	hdr[2] = byte(f.Channel >> 8)
	hdr[3] = byte(f.Channel)
	binary.BigEndian.PutUint32(hdr[4:8], uint32(len(f.Payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(f.Payload) == 0 {
		return nil
	}
	_, err := w.Write(f.Payload)
	return err
}

// DecodeFrame reads exactly one frame from r.
//
// Length is checked against MaxPayload BEFORE any payload bytes are
// read, so an oversize-frame attacker cannot exhaust memory by
// announcing a huge length. Flags are validated as in Frame.Validate.
func DecodeFrame(r io.Reader) (Frame, error) {
	var hdr [headerSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return Frame{}, ErrShortHeader
		}
		return Frame{}, err
	}
	f := Frame{
		Flags:   hdr[0],
		Channel: uint32(hdr[1])<<16 | uint32(hdr[2])<<8 | uint32(hdr[3]),
	}
	length := binary.BigEndian.Uint32(hdr[4:8])
	if length > MaxPayload {
		return Frame{}, fmt.Errorf("%w: announced %d", ErrOversizeFrame, length)
	}
	if f.Flags&flagReserved != 0 {
		return Frame{}, ErrReservedFlag
	}
	if f.Flags&FlagEnd == 0 {
		return Frame{}, ErrEndNotSet
	}
	if length > 0 {
		f.Payload = make([]byte, length)
		if _, err := io.ReadFull(r, f.Payload); err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return Frame{}, ErrShortPayload
			}
			return Frame{}, err
		}
	}
	return f, nil
}
