# examples — minimal wash app templates

Dead-basic, copy-to-start templates. Each shows the smallest complete wash
app: the BE⇄FE `app_msg` round-trip (receive a signal, transmit one back),
a hand-written vanilla-JS front end (no bundler), and a one-line build.
Grown over time.

| Example | Language | Uses | What it shows |
|---|---|---|---|
| [`go-about/`](go-about/) | Go | `internal/sdk` | window app: `sdk.Main`, `OnReady`/`OnAppMsg`, `go:embed` FE, framed probe |
| [`cpp-about/`](cpp-about/) | C++ | [`cpp-sdk`](../cpp-sdk/) | window app: `WireConn` handshake, `on_app_msg`/`send_app_msg`, CMake-embedded FE, `write_probe` |

Both define a `wash-app-*-about` custom element, embed `assets/index.js`
verbatim, and ship it in the `--wash-manifest` framed probe — no base64.

```sh
cd examples/go-about && make && ./out/go-about --wash-manifest   # framed header + raw index.js
cd examples/cpp-about && make && ./out/cpp-about --wash-manifest
```

To run one inside wash, drop the built binary into a scanned app dir
(`~/.local/share/wash/apps`) and launch it from the shell.

A settings panel (`panel.js` + `SettingsPanel` in the manifest) follows the
same shape — see `apps/vscode/fe` or `wash-display/fe` for a panel template.
