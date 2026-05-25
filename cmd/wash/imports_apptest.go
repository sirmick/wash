//go:build multicall && !no_app_test && wash_test_app

// wash-test is gated behind both !no_app_test (opt-out, like all
// other apps) AND wash_test_app (opt-in — the test app is hidden
// by default; the multi-call binary mirrors that by requiring the
// extra tag). `make multicall TEST_APP=1` adds it; otherwise it
// stays out of the registered set.
package main

import _ "github.com/sirmick/wash/apps/test/be"
