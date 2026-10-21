//go:build wash_no_asset_gzip

package router

// assetGzip is off in the in-browser VM build (WASH_NO_ASSET_GZIP=1, set by
// wash-vm/image/rootfs/build.sh). There the "wire" is a virtio-console in
// the same browser tab, so compression saves no transfer time, and deflate
// runs on the emulated CPU: it was ~60% of guest CPU for the two seconds
// after the desktop mounted.
const assetGzip = false
