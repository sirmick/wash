# Minimal build for the go-about example.
#
# The FE (assets/index.js) is hand-written vanilla JS — no bundler — so
# the whole build is `go build`, which embeds assets/ via go:embed and
# (through sdk.Main) ships it in the framed --wash-manifest probe.

out/go-about: main.go assets/index.js
	@mkdir -p out
	go build -o out/go-about .

.PHONY: manifest
manifest: out/go-about
	./out/go-about --wash-manifest | head -c 400; echo

.PHONY: clean
clean:
	rm -rf out
