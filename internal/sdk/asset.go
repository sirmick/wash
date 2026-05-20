package sdk

import (
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"path/filepath"

	"github.com/sirmick/wash/internal/wire"
)

// assetChunkBytes is the raw chunk size for asset.data frames. Base64
// inflation gives ~256 KiB per frame, far under the 16 MiB cap and
// small enough not to pin large allocations on the SDK side.
const assetChunkBytes = 192 * 1024

// serveAsset opens the named asset from AppDef.Assets and streams it
// back to the router as AssetReadOK + AssetData chunks (WIRE.md §7).
// Missing assets reply with AssetReadErr.
func (c *Conn) serveAsset(req wire.AssetRead) error {
	if c.def.Assets == nil {
		return c.writeCtrl(wire.NewAssetReadErr(req.ID, wire.ErrCodeNotFound, "app has no embedded assets"))
	}
	data, mtype, err := readAsset(c.def.Assets, req.Name)
	if err != nil {
		return c.writeCtrl(wire.NewAssetReadErr(req.ID, wire.ErrCodeNotFound, err.Error()))
	}
	if err := c.writeCtrl(wire.NewAssetReadOK(req.ID, int64(len(data)), mtype)); err != nil {
		return err
	}
	// Chunked base64.
	for off := 0; off < len(data) || off == 0; {
		end := off + assetChunkBytes
		if end > len(data) {
			end = len(data)
		}
		b64 := base64.StdEncoding.EncodeToString(data[off:end])
		last := end == len(data)
		if err := c.writeCtrl(wire.NewAssetData(req.ID, b64, last)); err != nil {
			return err
		}
		if last {
			return nil
		}
		off = end
	}
	return nil
}

// readAsset reads name from fsys and returns the bytes plus a MIME
// type derived from the extension. Backed by io/fs so apps can pass
// an embed.FS, an os.DirFS, or anything else.
func readAsset(fsys fs.FS, name string) ([]byte, string, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, "", fmt.Errorf("open %s: %w", name, err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", name, err)
	}
	mtype := mime.TypeByExtension(filepath.Ext(name))
	if mtype == "" {
		mtype = "application/octet-stream"
	}
	return data, mtype, nil
}
