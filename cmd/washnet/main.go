// Command washnet runs wash-net's pure config transforms from the shell — the
// dev/debug counterpart of the golden tests (docs/NET.md §9). It reads a config
// from one representation and writes another, all through the model IR:
//
//	washnet --in-uci network.uci,wireless.uci --out nm     # transpile+compile: UCI → NM keyfiles
//	washnet --in-nm wg0.nmconnection           --out uci   # read+transpile:   NM → UCI
//	washnet --in-uci network.uci               --out uci   # round-trip UCI through the IR
//
// UCI inputs are keyed by package (the filename stem: network.uci → "network").
// NM inputs are keyed by connection id (the filename stem).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sirmick/wash/internal/washnet/codec"
	"github.com/sirmick/wash/internal/washnet/model"
	"github.com/sirmick/wash/internal/washnet/nmprofile"
)

func main() {
	inUCI := flag.String("in-uci", "", "comma-separated UCI files (keyed by package = filename stem)")
	inNM := flag.String("in-nm", "", "comma-separated NM .nmconnection files (keyed by id = filename stem)")
	out := flag.String("out", "nm", "output representation: nm | uci")
	flag.Parse()

	cfg, err := load(*inUCI, *inNM)
	if err != nil {
		fmt.Fprintln(os.Stderr, "washnet:", err)
		os.Exit(1)
	}

	switch *out {
	case "nm":
		kfs, err := nmprofile.RenderKeyfiles(cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "washnet: render nm:", err)
			os.Exit(1)
		}
		emit(kfs, ".nmconnection")
	case "uci":
		files, err := codec.Render(cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "washnet: render uci:", err)
			os.Exit(1)
		}
		emit(files, ".uci")
	default:
		fmt.Fprintln(os.Stderr, "washnet: --out must be nm or uci")
		os.Exit(2)
	}
}

func load(inUCI, inNM string) (model.Config, error) {
	switch {
	case inUCI != "" && inNM != "":
		return model.Config{}, fmt.Errorf("pass only one of --in-uci / --in-nm")
	case inUCI != "":
		files, err := readKeyed(inUCI)
		if err != nil {
			return model.Config{}, err
		}
		return codec.Parse(files)
	case inNM != "":
		files, err := readKeyed(inNM)
		if err != nil {
			return model.Config{}, err
		}
		return nmprofile.ParseKeyfiles(files)
	}
	return model.Config{}, fmt.Errorf("need --in-uci or --in-nm")
}

// readKeyed reads each comma-separated path, keyed by its filename stem.
func readKeyed(list string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range strings.Split(list, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		stem := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		out[stem] = string(b)
	}
	return out, nil
}

// emit prints each rendered artifact with a header, in stable key order.
func emit(files map[string]string, ext string) {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("# ==== %s%s ====\n%s\n", k, ext, files[k])
	}
}
