package agentd

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/agentproto"
)

// agentd handles exactly the requests agentproto registers: a handler for a
// kind the protocol does not list is a message no document describes, and a
// registered request nothing handles is a promise nothing keeps.
func TestHandlersMatchTheRegistry(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	handled := map[string]bool{}
	re := regexp.MustCompile(`sdk\.Handle(?:From)?Void\(bus, (?:"([a-z_.]+)"|FocusKind),`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			kind := m[1]
			if kind == "" {
				kind = FocusKind
			}
			handled[kind] = true
		}
	}
	// The StateService registers these itself.
	handled["subscribe"], handled["unsubscribe"] = true, true

	var registered []string
	for _, m := range agentproto.Messages {
		if m.Dir != agentproto.Request {
			continue
		}
		registered = append(registered, m.Kind)
		if !handled[m.Kind] {
			t.Errorf("request %q is registered but agentd has no handler for it", m.Kind)
		}
	}
	for kind := range handled {
		if !slices.Contains(registered, kind) {
			t.Errorf("agentd handles %q, which agentproto does not register", kind)
		}
	}
}
