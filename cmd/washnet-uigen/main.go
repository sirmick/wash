// Command washnet-uigen writes the FE codegen artifacts (UI descriptor, TS
// types, i18n scaffold) derived from the washnet model. Go structs are the
// single source of truth (docs/NET.md §6); this is the one-direction codegen
// step. Run it to regenerate apps/net/fe/src/generated.
//
//	go run ./cmd/washnet-uigen -out apps/net/fe/src/generated
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sirmick/wash/internal/washnet/uigen"
)

func main() {
	out := flag.String("out", "apps/net/fe/src/generated", "output directory")
	flag.Parse()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	files := map[string]string{
		"descriptor.json": uigen.DescriptorJSON(),
		"types.ts":        uigen.EmitTS(),
		"i18n.json":       uigen.I18nJSON(),
	}
	for name, content := range files {
		path := filepath.Join(*out, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("wrote", path)
	}
}
