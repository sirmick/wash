// The shell's `window.wash` API, as the browser-context callbacks in these
// specs see it.
//
// Without this, every `page.evaluate((...) => window.wash.…)` was an
// implicit `any`: a typo in an API name, or a signature that drifted after
// the shell changed, compiled clean and failed at runtime as an
// undefined-is-not-a-function deep inside a test. Pointing at the SAME
// declaration the apps use means the e2e suite now fails to typecheck when
// the shell API changes under it, which is the earliest place to find out.
/// <reference path="../../web/lib/src/window-wash.d.ts" />
