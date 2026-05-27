package services

// procdBackend will wrap OpenWRT's procd init. Stub for now —
// `newProcd()` always returns nil so Detect() falls through to a
// real backend or to nil. Sized so a real impl can drop in without
// touching backend.go or app.go.
//
// What a full impl would do:
//   - Detect: presence of /sbin/procd + walkable /etc/init.d.
//   - List: walk /etc/init.d, ask `ubus call service list` for the
//     running state. Enabled = symlink in /etc/rc.d/S* exists.
//   - ActionArgv: /etc/init.d/<name> {start|stop|restart|reload|
//     enable|disable}. Procd scripts dispatch verbs to the same
//     script.
//   - LogAppID: com.wash.syslogs (OpenWRT logread → /var/log).

func newProcd() Backend { return nil }
