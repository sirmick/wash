// Command washnet-edit is the human round-trip of the CLI loop, in one command
// (the `virsh edit` / `visudo` pattern): it reads the box's current networking
// as UCI, opens it in $EDITOR, validates + diffs the result, and applies it with
// an interactive commit-confirm — apply, then "keep?" with an auto-revert if you
// don't confirm in time, so a fat-fingered gateway that drops your SSH undoes
// itself. Backend is WASH_NETD_BACKEND or, unset, autodetected.
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/sirmick/wash/apps/netd/be/backendsel"
	"github.com/sirmick/wash/internal/washnet/backend"
	"github.com/sirmick/wash/internal/washnet/change"
	"github.com/sirmick/wash/internal/washnet/codec"
	"github.com/sirmick/wash/internal/washnet/ucibuf"
	"github.com/sirmick/wash/internal/washnet/validate"
)

const confirmWindow = 30 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "washnet-edit:", err)
		os.Exit(1)
	}
}

func run() error {
	name, src := os.Getenv("WASH_NETD_BACKEND"), "env"
	if name == "" {
		var why string
		name, why = backendsel.Autodetect()
		src = "autodetect: " + why
	}
	fmt.Fprintf(os.Stderr, "washnet-edit: backend=%s (%s)\n", name, src)
	a := backendsel.New(name)
	if a == nil {
		return fmt.Errorf("no live backend for %q", name)
	}

	cur := a.Live()
	files, err := codec.Render(cur)
	if err != nil {
		return fmt.Errorf("render current: %w", err)
	}

	edited, err := editBuffer(ucibuf.Marshal(files))
	if err != nil {
		return err
	}
	newCfg, err := codec.Parse(ucibuf.Unmarshal(edited))
	if err != nil {
		return fmt.Errorf("parse edited UCI: %w", err)
	}

	// Capability check (the CLI's grey-out).
	bad := false
	for _, d := range validate.Validate(newCfg, a.Capabilities()) {
		if d.Severity == validate.Error {
			fmt.Fprintf(os.Stderr, "  error: %s\n", d.Message)
			bad = true
		}
	}
	if bad {
		return fmt.Errorf("config rejected by the %s backend's capabilities", name)
	}

	diff := change.Compute(cur, newCfg)
	if len(diff.Entries) == 0 {
		fmt.Println("no changes.")
		return nil
	}
	fmt.Println("changes:")
	for _, e := range diff.Entries {
		sym := map[change.Op]string{change.OpAdd: "+", change.OpRemove: "-", change.OpUpdate: "~"}[e.Op]
		fmt.Printf("  %s %s %s\n", sym, e.Kind, e.Name)
	}
	if !prompt(fmt.Sprintf("apply these to the %s backend? [y/N] ", name)) {
		fmt.Println("aborted.")
		return nil
	}

	token, err := a.Apply(backend.RenderPlan{Target: newCfg})
	if err != nil {
		return fmt.Errorf("apply failed: %w", err)
	}
	if err := a.Verify(newCfg); err != nil {
		if rerr := a.Rollback(token); rerr != nil {
			return fmt.Errorf("ROLLBACK FAILED after failed verify: %v (verify: %w)", rerr, err)
		}
		return fmt.Errorf("reverted (verify failed): %w", err)
	}

	// Interactive keep-confirm: revert unless explicitly kept in time — the safe
	// default if you've just locked yourself out.
	if keepConfirm() {
		if err := a.Confirm(token); err != nil {
			return fmt.Errorf("confirm: %w", err)
		}
		fmt.Println("kept.")
		return nil
	}
	if err := a.Rollback(token); err != nil {
		return fmt.Errorf("rollback: %w", err)
	}
	fmt.Println("reverted.")
	return nil
}

// editBuffer writes s to a temp file, opens $EDITOR on it, and returns the result.
func editBuffer(s string) (string, error) {
	f, err := os.CreateTemp("", "washnet-*.uci")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(s); err != nil {
		f.Close()
		return "", err
	}
	f.Close()

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command(editor, f.Name())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("editor %s: %w", editor, err)
	}
	b, err := os.ReadFile(f.Name())
	return string(b), err
}

func prompt(msg string) bool {
	fmt.Print(msg)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return line == "y\n" || line == "Y\n"
}

// keepConfirm asks the user to keep the just-applied change, reverting on timeout
// (lock-out protection: no answer ⇒ undo).
func keepConfirm() bool {
	fmt.Printf("keep these changes? [y to keep, auto-revert in %s] ", confirmWindow)
	ch := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		ch <- line
	}()
	select {
	case line := <-ch:
		return line == "y\n" || line == "Y\n"
	case <-time.After(confirmWindow):
		fmt.Println("\ntimed out")
		return false
	}
}
