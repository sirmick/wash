package router

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
)

// A watch streams each message the instance sends its frontend, in order,
// then says the instance is gone.
func TestControlWatchStreamsUntilGone(t *testing.T) {
	r := NewRouter(Config{NoSession: true}, NewRegistry(), func(string, ...any) {})
	r.registerApp(&AppInstance{AppID: "com.wash.ai", InstanceID: "i-1", Manifest: &Manifest{ID: "com.wash.ai"}, router: r})
	client, server := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() { r.controlWatch(context.Background(), server, "i-1"); close(done) }()
	rd := bufio.NewReader(client)
	next := func() map[string]any {
		t.Helper()
		line, err := rd.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if m := next(); m["t"] != "watching" {
		t.Fatalf("first line %v", m)
	}
	r.feedAppMsgStreams("i-1", []byte(`{"kind":"state","n":1}`))
	r.feedAppMsgStreams("i-1", []byte(`{"kind":"state","n":2}`))
	for want := 1.0; want <= 2; want++ {
		m := next()
		data, _ := m["data"].(map[string]any)
		if m["t"] != "msg" || data["n"] != want {
			t.Fatalf("message %v: %v", want, m)
		}
	}
	r.endAppMsgStreams("i-1")
	if m := next(); m["t"] != "gone" {
		t.Fatalf("after the end: %v", m)
	}
	<-done
}

// An unknown instance is refused, and a watch that falls behind is ended
// and marked, never silently thinned.
func TestControlWatchRefusesAndOverflows(t *testing.T) {
	r := NewRouter(Config{NoSession: true}, NewRegistry(), func(string, ...any) {})
	client, server := net.Pipe()
	go r.controlWatch(context.Background(), server, "i-missing")
	line, _ := bufio.NewReader(client).ReadBytes('\n')
	var m map[string]any
	_ = json.Unmarshal(line, &m)
	if m["code"] != "not_found" {
		t.Fatalf("unknown instance: %v", m)
	}
	client.Close()

	s, cancel := r.WatchAppMsgs("i-2")
	defer cancel()
	for i := 0; i <= watchBuffer; i++ {
		r.feedAppMsgStreams("i-2", []byte(`{}`))
	}
	select {
	case <-s.ended:
	default:
		t.Fatal("a full watch was not ended")
	}
	if !s.overflowed {
		t.Fatal("the end was not marked as overflow")
	}
}
