// Package proto is the tiny length-prefixed frame protocol spoken between the
// host harness (wash-vm/vm) and the in-guest agent (cmd/washvm-agent) over a
// serial control plane. Frames are a 4-byte big-endian length followed by JSON.
// This is the out-of-band control channel of docs/NET.md §8 in its simplest
// bring-up form; it grows toward the multi-plane virtio-console transport.
package proto

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// maxFrame bounds a single frame to guard against a desynced stream.
const maxFrame = 16 << 20

// Request is a command for the guest agent to run.
type Request struct {
	ID  uint64 `json:"id"`
	Cmd string `json:"cmd"`
}

// Response is the result of running a Request.
type Response struct {
	ID   uint64 `json:"id"`
	Exit int    `json:"exit"`
	Out  string `json:"out"`
	Err  string `json:"err"`
}

// WriteFrame marshals v to JSON and writes a length-prefixed frame.
func WriteFrame(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// ReadFrame reads one length-prefixed frame and unmarshals it into v.
func ReadFrame(r io.Reader, v any) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxFrame {
		return fmt.Errorf("proto: frame too large (%d bytes)", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	return json.Unmarshal(buf, v)
}
