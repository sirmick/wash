package agentproto

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// A push goes on the wire as its fields plus "kind", which is what every
// frontend switches on.
func TestEncodeAddsTheKind(t *testing.T) {
	raw, spec, err := Encode(SessionState{Key: "acp:1", State: State{Version: Version}})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Kind != "session_state" || !strings.HasPrefix(string(raw), `{"kind":"session_state","key":"acp:1",`) {
		t.Fatalf("encoded %s as %s", spec.Kind, raw)
	}
	var back SessionState
	if err := json.Unmarshal(raw, &back); err != nil || back.Key != "acp:1" || back.State.Version != Version {
		t.Fatalf("round trip = %+v, %v", back, err)
	}
	if raw, _, err := Encode(Subscribe{}); err == nil {
		t.Fatalf("a request type was encoded as a push: %s", raw)
	}
	if _, _, err := Encode(struct{ X int }{}); err == nil {
		t.Fatal("an unregistered type was encoded")
	}
}

// Every registered message has a kind, a struct payload and a description,
// and a push's payload type belongs to exactly one kind (Encode looks the
// kind up by type).
func TestRegistryIsWellFormed(t *testing.T) {
	pushTypes := map[reflect.Type]string{}
	for _, m := range Messages {
		if m.Kind == "" || m.Doc == "" || m.From == "" {
			t.Errorf("%+v lacks a kind, a description or a sender", m)
		}
		pt := reflect.TypeOf(m.Payload)
		if pt.Kind() != reflect.Struct {
			t.Errorf("%s payload is %s, want a struct", m.Kind, pt)
		}
		if m.Dir == Push {
			if other, dup := pushTypes[pt]; dup {
				t.Errorf("%s and %s share the payload type %s", other, m.Kind, pt)
			}
			pushTypes[pt] = m.Kind
			if _, has := pt.FieldByName("Kind"); has {
				t.Errorf("%s payload has its own Kind field; Encode adds kind", m.Kind)
			}
		}
		if _, err := json.Marshal(m.Payload); err != nil {
			t.Errorf("%s payload does not encode: %v", m.Kind, err)
		}
	}
}

func TestLookup(t *testing.T) {
	if s, ok := Lookup(Push, "manager_state"); !ok || reflect.TypeOf(s.Payload) != reflect.TypeOf(ManagerState{}) {
		t.Fatalf("Lookup(push, manager_state) = %+v, %v", s, ok)
	}
	if _, ok := Lookup(Request, "manager_state"); ok {
		t.Fatal("found a push as a request")
	}
}
