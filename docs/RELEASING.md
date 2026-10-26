# Releasing wash

A release is one version commit and one annotated tag. CI does the rest: on a
`vX.Y.Z` tag the `release` job in `.github/workflows/ci.yml` publishes the
.deb/.rpm/.apk packages, built by the `package` matrix, to the GitHub Releases
page.

## Where the version lives

The root `VERSION` file is the source of truth. Everything that ships either
derives from it at build time or is checked against it.

| Place | How it gets the version | Guarded by |
|---|---|---|
| Go binaries (router, apps, About) | `-ldflags -X internal/version.Version=$(VERSION)` (Makefile `GOFLAGS`) | derived |
| `internal/version/version.go` | literal, the default for a bare `go build` | `make check-versions` |
| `wash-display` (C++ compositor) | `-DWASH_VERSION=$(VERSION)` passed to CMake; a bare cmake build says `dev` | derived |
| `debian/changelog` (top entry) | literal | `make check-versions` |
| `rpm/wash.spec` (`Version:` and `%changelog`) | literal | `make check-versions` (Version only) |
| `alpine/APKBUILD` (`pkgver=`) | literal | `make check-versions` |
| packaging scripts (`run_matrix.sh`, `make-source-tarball.sh`) | read `VERSION` (or `WASH_PKG_VERSION`) | derived |
| `README.md` | literal, three places: "Current version", `wash-install vX.Y.Z`, `wash_X.Y.Z-1_amd64.deb` | not guarded |

Not release versions, so leave them: the frontend `package.json` versions
(private, never read), test-fixture app versions (`"0.9.0"` in Go tests), and
historical mentions in docs ("measured on 0.20.0", "shipped in 0.9.0").

If you add a component that reports a version, stamp it from `VERSION` at build
time like the two above. Don't add another literal to keep in step.

## Steps

1. Pick the number. Use a minor bump (0.21.0) when there are new features,
   and a patch bump (0.21.1) for fixes only. List what's in it with
   `git log --oneline --no-merges vPREV..main`.
2. Edit the literals:
   - `VERSION`
   - `internal/version/version.go`
   - `alpine/APKBUILD` `pkgver=`
   - `rpm/wash.spec` `Version:`
   - `README.md`, all three places
   - a new top entry in `debian/changelog`. The date is from
     `date -u '+%a, %d %b %Y %H:%M:%S +0000'`, and the name and email must
     match the other entries.
   - a new first entry under `%changelog` in `rpm/wash.spec`. The date is
     from `date -u '+%a %b %d %Y'`, and a blank line separates it from the
     previous entry.

   The two changelogs say the same things. The deb entry is the full one and
   the rpm entry the short one.
3. `make check-versions` must print that deb/rpm/apk and the Go default
   match. Then `make unit-test` (it runs `check-versions` too) and
   `make e2e-test`.
4. Commit the bump as `X.Y.Z`, with a short summary paragraph as the body.
5. Tag it: `git tag -a vX.Y.Z -m "wash X.Y.Z" -m "<the same summary>"`.
6. Push both: `git push origin main vX.Y.Z`. Then watch the `ci` run for the
   tag. `release` only runs once every `package` row is green, and then the
   Releases page has the stable names (`wash-debian-13-amd64.deb`, …) and
   the native versioned ones.

## On a box with limited RAM

The full builds fan out to `nproc` jobs, and Playwright runs up to 8 workers.
Turn both down:
`JOBS=4 GOMAXPROCS=4 make wash`, and for e2e run Playwright with
`--workers=3`.
