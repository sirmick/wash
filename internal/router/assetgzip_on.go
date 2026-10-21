//go:build !wash_no_asset_gzip

package router

// assetGzip turns on gzipIfSmaller. The default: over a real network the
// smaller wire bytes are worth the one-time compression.
const assetGzip = true
