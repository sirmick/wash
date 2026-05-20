package sdk

import (
	"context"
	"fmt"
	"io"
	"io/fs"
)

// uploadBundle ships the embedded JS bundle (index.js) to the router
// via a one-shot raw channel marked Kind=bundle. The router caches
// the bytes per-instance and replays them to every (re)attaching
// shell — replacing the old base64-in-JSON asset.fetch / asset.deliver
// pipeline.
//
// Called from sdk.Main in a goroutine after handshake. No-op if the
// app has no embedded assets (e.g. tests that pass a nil Assets).
func (c *Conn) uploadBundle() error {
	if c.def.Assets == nil {
		return nil
	}
	data, err := readBundleBytes(c.def.Assets)
	if err != nil {
		return fmt.Errorf("read bundle: %w", err)
	}
	if len(data) == 0 {
		return nil
	}
	ch, err := c.openChannelKind(context.Background(), c.windowID, bundleChannelKind)
	if err != nil {
		return fmt.Errorf("open bundle channel: %w", err)
	}
	// Chunk on app→router side too, mostly to keep the per-frame
	// memory copy small.
	const chunkSize = 256 * 1024
	for off := 0; off < len(data); off += chunkSize {
		end := off + chunkSize
		if end > len(data) {
			end = len(data)
		}
		if _, werr := ch.Write(data[off:end]); werr != nil {
			_ = ch.Close()
			return fmt.Errorf("write bundle: %w", werr)
		}
	}
	return ch.Close()
}

// bundleChannelKind mirrors wire.ChannelKindBundle — imported as a
// constant here so callers don't have to reach into wire.
const bundleChannelKind = "bundle"

// readBundleBytes loads index.js from the app's embedded fs.
func readBundleBytes(fsys fs.FS) ([]byte, error) {
	f, err := fsys.Open("index.js")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
