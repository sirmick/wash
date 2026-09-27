// Command qa-split converts a single-file Wash QA document (API 3.x, one
// QA.md with a checkpoint) into the one-file-per-thread directory Wash 4
// reads, once, outside Wash:
//
//	go run ./tools/qa-split -root ~/redoubt ~/redoubt/.wash/QA.md ~/redoubt/.wash/qa
//
// Threads keep their events; paths under -root and the home directory are
// written as "." and "~". The old file is not changed.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirmick/wash/internal/swarm"
)

const checkpointV1 = "<!-- wash-qa-checkpoint-v1: "

type archiveV1 struct {
	Threads   []swarm.QAThread  `json:"threads"`
	Authors   map[string]string `json:"authors"`
	Decisions []swarm.Message   `json:"decisions,omitempty"`
}

func main() {
	root := flag.String("root", "", "the project root, written as \".\"")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: qa-split [-root dir] QA.md out-dir")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 2 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(flag.Arg(0), flag.Arg(1), *root); err != nil {
		fmt.Fprintln(os.Stderr, "qa-split:", err)
		os.Exit(1)
	}
}

func run(in, out, root string) error {
	b, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	i := bytes.LastIndex(b, []byte("\n"+checkpointV1))
	if i < 0 {
		return fmt.Errorf("%s has no Wash QA checkpoint", in)
	}
	payload := strings.TrimSuffix(strings.TrimSpace(string(b[i+1+len(checkpointV1):])), " -->")
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return fmt.Errorf("checkpoint: %w", err)
	}
	var a archiveV1
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("checkpoint: %w", err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	for _, q := range a.Threads {
		if !swarm.ValidProfileName(q.ID) {
			return fmt.Errorf("thread id %q cannot be a file name", q.ID)
		}
		path := filepath.Join(out, q.ID+".md")
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("%s exists; convert into an empty directory", path)
		}
		f := swarm.QAFile{Thread: q, Authors: a.Authors}
		for _, d := range a.Decisions {
			if d.Thread == q.ID {
				f.Decisions = append(f.Decisions, d)
			}
		}
		content, err := swarm.EncodeQAFile(f, root, home)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("%d threads written to %s\n", len(a.Threads), out)
	return nil
}
