// Package agentproto is the protocol between com.wash.agentd and its
// frontends, written down once: every request a frontend sends agentd and
// every message agentd sends back, as Go structs with json tags.
//
// It is the single source. The TypeScript types frontends use are generated
// from it (`make gen-agent-protocol`, web/lib/src/agent-protocol.gen.ts),
// and so are the message tables in docs/AGENT_PROTOCOL.md; `make
// check-agent-protocol` fails when either is stale. agentd's handlers
// decode into these types and every push goes through Send, so the Go, the
// TypeScript and the document cannot say different things about a message.
package agentproto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// Dir is which way a message travels.
type Dir string

const (
	// Request is sent by a frontend (or another app) to agentd.
	Request Dir = "request"
	// Push is sent by agentd.
	Push Dir = "push"
	// DesktopDir is something agentd asks of the desktop (desktop.go), handled
	// in one place rather than sent to a frontend.
	DesktopDir Dir = "event"
)

// Class is the router's queueing class for a push (docs/QOS.md §3).
type Class string

const (
	Interactive Class = "interactive"
	// Bulk can be overtaken by Interactive traffic: transcript streams and
	// usage counters, which must never sit ahead of typing or a question.
	Bulk Class = "bulk"
)

// Spec describes one message.
type Spec struct {
	Kind string
	Dir  Dir
	// Payload is the message's type, as a zero value. Its fields are the
	// message's fields; "kind" is added on the wire.
	Payload any
	// From says who may send a request, or who a push is sent to.
	From string
	// Reply names what agentd sends back for a request, if anything.
	Reply string
	// Class is a push's queueing class; Interactive when empty.
	Class Class
	// Keyed pushes are about one session and carry its roster key, so a
	// frontend showing another session drops them.
	Keyed bool
	Doc   string
}

// Messages is every message, requests first. Order is the document's.
var Messages []Spec

func register(s Spec) {
	for _, m := range Messages {
		if m.Kind == s.Kind && m.Dir == s.Dir {
			panic("agentproto: duplicate " + string(s.Dir) + " " + s.Kind)
		}
	}
	Messages = append(Messages, s)
}

// Lookup finds a message by direction and kind.
func Lookup(dir Dir, kind string) (Spec, bool) {
	for _, m := range Messages {
		if m.Dir == dir && m.Kind == kind {
			return m, true
		}
	}
	return Spec{}, false
}

// AppID is agentd's app id: where requests are sent.
const AppID = "com.wash.agentd"

// kindOf is the message registered for a payload's type. Payload types are
// unique across the registry, so a value names its own kind.
func kindOf(m any) (Spec, error) {
	t := reflect.TypeOf(m)
	for _, s := range Messages {
		if reflect.TypeOf(s.Payload) == t {
			return s, nil
		}
	}
	return Spec{}, fmt.Errorf("agentproto: %s is not a registered message", t)
}

// Encode renders a message as it goes on the wire: its fields plus "kind".
func Encode(m any) (json.RawMessage, Spec, error) {
	spec, err := kindOf(m)
	if err != nil {
		return nil, spec, err
	}
	body, err := json.Marshal(m)
	if err != nil {
		return nil, spec, err
	}
	var out bytes.Buffer
	out.WriteString(`{"kind":`)
	k, _ := json.Marshal(spec.Kind)
	out.Write(k)
	if len(body) > 2 {
		out.WriteByte(',')
		out.Write(body[1:])
	} else {
		out.WriteByte('}')
	}
	return out.Bytes(), spec, nil
}

// Send delivers one message to one recipient, in its registered class: a
// push from agentd to a frontend, or (with SendAgentd) a request to agentd.
func Send(c *sdk.Conn, to wire.Recipient, m any) error {
	raw, spec, err := Encode(m)
	if err != nil {
		return err
	}
	if spec.Class == Bulk {
		return c.SendAppMsgToBulk(to, raw)
	}
	return c.SendAppMsgTo(to, raw)
}

// SendAgentd sends a request to agentd.
func SendAgentd(c *sdk.Conn, m any) error {
	return Send(c, wire.Recipient{AppID: AppID}, m)
}

// Decode reads a message's payload into out, for a Go frontend that
// receives agentd's pushes as the generic value the SDK hands it.
func Decode(data any, out any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
