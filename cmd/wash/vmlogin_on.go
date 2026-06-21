//go:build multicall && washvmlogin

// vmlogin dispatch, compiled in only with -tags=washvmlogin. This pulls in
// internal/runner/vmlogin → github.com/sirmick/wash/wash-vm/guest, the
// in-browser / wemu VM login front. The distro source tarball prunes wash-vm/,
// so the default packaging multicall is built WITHOUT this tag; the VM image
// build (wash-vm/image/rootfs/build.sh, WASH_VMLOGIN=1) turns it on.
package main

import vmloginrun "github.com/sirmick/wash/internal/runner/vmlogin"

func init() { vmloginRun = vmloginrun.Run }
