// `wash agent-hooks install|remove|status` — the headless face of the
// hook installer (docs/AGENT_TERM.md §4). The Settings → Agents panel
// will call the same Install/Remove/Status functions; this exists so a
// remote box with no browser in front of it is one ssh command away from
// agent-aware terminals.
package agenthook

import (
	"flag"
	"fmt"
	"io"
	"os"
)

// RunCLI implements the `wash agent-hooks …` verb. Returns a process
// exit code.
func RunCLI(args []string) int {
	fs := flag.NewFlagSet("wash agent-hooks", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("path", "", "settings file to edit (default: $CLAUDE_CONFIG_DIR/settings.json, else ~/.claude/settings.json)")
	command := fs.String("command", "", "wash-agent-hook path to install (default: resolved next to this binary, then $PATH)")
	dryRun := fs.Bool("dry-run", false, "print what would be written; change nothing")
	verb := ""
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		verb, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "wash agent-hooks:", err)
		cliUsage(os.Stderr)
		return 2
	}
	if verb == "" {
		// Flags came first (`wash agent-hooks --path X install`).
		verb = fs.Arg(0)
	}
	if *path == "" {
		*path = SettingsPath()
	}
	if *path == "" {
		fmt.Fprintln(os.Stderr, "wash agent-hooks: cannot locate a settings file (no $HOME); pass --path")
		return 1
	}
	if *command == "" {
		*command = HelperPath()
	}

	switch verb {
	case "install":
		return cliInstall(*path, *command, *dryRun)
	case "remove", "uninstall":
		return cliRemove(*path, *dryRun)
	case "status", "":
		return cliStatus(*path)
	case "help", "-h", "--help":
		cliUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "wash agent-hooks: unknown verb %q\n", verb)
		cliUsage(os.Stderr)
		return 2
	}
}

func cliUsage(w io.Writer) {
	fmt.Fprintln(w, `wash agent-hooks — teach Claude Code to report its state to wash terminals

  wash agent-hooks status     show which hooks are installed (default)
  wash agent-hooks install    merge wash's hook entries into the settings file
  wash agent-hooks remove     take them back out again

Flags:
  --path <file>     settings file (default $CLAUDE_CONFIG_DIR/settings.json,
                    else ~/.claude/settings.json)
  --command <path>  wash-agent-hook binary to install
  --dry-run         print the merged file instead of writing it

The merge is additive and idempotent: other hooks and settings are left
alone, and remove only deletes entries wash added. Claude Code re-reads
its settings file live, so running sessions pick this up without a
restart.`)
}

func cliInstall(path, command string, dryRun bool) int {
	settings, err := LoadSettings(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wash agent-hooks:", err)
		return 1
	}
	added, updated := Install(settings, command, ClaudeHooks)
	if dryRun {
		data, err := EncodeSettings(settings)
		if err != nil {
			fmt.Fprintln(os.Stderr, "wash agent-hooks:", err)
			return 1
		}
		fmt.Printf("# %s (dry run: %d to add, %d to correct)\n", path, added, updated)
		os.Stdout.Write(data)
		return 0
	}
	if added == 0 && updated == 0 {
		fmt.Printf("already installed: %d hooks in %s\n", len(ClaudeHooks), path)
		return 0
	}
	if err := SaveSettings(path, settings); err != nil {
		fmt.Fprintln(os.Stderr, "wash agent-hooks:", err)
		return 1
	}
	fmt.Printf("installed %d hook(s), corrected %d, in %s\n", added, updated, path)
	fmt.Printf("  helper: %s\n", command)
	fmt.Println("  Claude Code reloads settings live — running sessions are covered.")
	return 0
}

func cliRemove(path string, dryRun bool) int {
	settings, err := LoadSettings(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wash agent-hooks:", err)
		return 1
	}
	removed := Remove(settings)
	if dryRun {
		data, err := EncodeSettings(settings)
		if err != nil {
			fmt.Fprintln(os.Stderr, "wash agent-hooks:", err)
			return 1
		}
		fmt.Printf("# %s (dry run: %d to remove)\n", path, removed)
		os.Stdout.Write(data)
		return 0
	}
	if removed == 0 {
		fmt.Printf("nothing to remove in %s\n", path)
		return 0
	}
	if err := SaveSettings(path, settings); err != nil {
		fmt.Fprintln(os.Stderr, "wash agent-hooks:", err)
		return 1
	}
	fmt.Printf("removed %d wash hook(s) from %s\n", removed, path)
	return 0
}

func cliStatus(path string) int {
	settings, err := LoadSettings(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wash agent-hooks:", err)
		return 1
	}
	states, stray := Status(settings, ClaudeHooks)
	fmt.Println(path)
	installed := 0
	for _, st := range states {
		mark := "not installed"
		switch {
		case st.Installed && st.Spec.Async && !st.Async:
			// A status hook that lost its async flag WOULD stall a turn.
			// (decide is synchronous by design — the agent waits for the
			// answer — so its missing flag is correct, not a warning.)
			mark = "installed (WARNING: not async — it will block turns)"
			installed++
		case st.Installed:
			mark = "installed"
			installed++
		}
		name := st.Spec.Event
		if st.Spec.Matcher != "" {
			name += " [" + st.Spec.Matcher + "]"
		}
		fmt.Printf("  %-34s %s\n", name, mark)
	}
	for _, event := range stray {
		fmt.Printf("  %-34s stray wash entry (remove will clear it)\n", event)
	}
	switch {
	case installed == 0:
		fmt.Println("run `wash agent-hooks install` to enable agent status in wash terminals")
		return 1
	case installed < len(states):
		fmt.Println("partially installed — run `wash agent-hooks install` to complete it")
		return 1
	}
	fmt.Printf("all %d hooks installed\n", installed)
	return 0
}
