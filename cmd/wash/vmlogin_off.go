//go:build multicall && !washvmlogin

// Default packaging multicall: vmlogin is NOT compiled in (it would pull in
// wash-vm/guest, pruned from the distro source tarball). vmloginRun stays nil
// and `wash-vmlogin` dispatch falls through to the registry/not-found path.
package main

func init() { vmloginRun = nil }
