// The URL pattern wash uses to find links in terminal output.
//
// @xterm/addon-web-links ships its own, and it is too narrow: its body
// class excludes ( ) ! * and ' outright, so it stops at the first one and
// hands back a truncated URL. That is worst in a query string, where those
// characters are ordinary — `?q=a!b` opened `?q=a`, and a Maps link
// (`/data=!3m1!4b1`) lost everything after `=`. The failure is silent: a
// page loads, just not the one that was clicked.
//
// So: allow those characters in the body, and deal with the two real
// reasons the old pattern excluded them.
//
//   - Brackets must not swallow a closing delimiter from the prose around
//     the URL. `(http://x.io/a)` should link `http://x.io/a`, but
//     `.../Foo_(bar)` should keep its parens. Both fall out of matching
//     BALANCED groups as single atoms: a `(` that has no partner is not an
//     atom, so the match ends before it.
//   - Trailing punctuation is usually the sentence's, not the URL's. The
//     lookbehind refuses to end on any of it; the `+` then gives back
//     characters until it can, which is exactly "trim the tail".
//
// Not in the body at all: whitespace, angle brackets, quotes, backtick,
// braces, pipe and backslash — these delimit rather than appear in URLs,
// and letting them in makes a smeared TUI line into one enormous "link".
//
// No `g` flag: the addon does `new RegExp(source, flags + 'g')` itself,
// and a doubled flag throws.
export const TERM_URL_RE =
  /https?:\/\/(?:\([^\s()<>]*\)|\[[^\s[\]<>]*\]|[^\s()<>[\]{}|\\^"`])+(?<![-.,;:!?'*_~+])/i;
