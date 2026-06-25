package wire

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"
)

// roundtripCtrl marshals m, decodes it back via DecodeCtrl, and
// asserts the decoded value deep-equals m.
func roundtripCtrl(t *testing.T, m any) {
	t.Helper()
	b, err := EncodeCtrl(m)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeCtrl(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(got, m) {
		t.Fatalf("round-trip mismatch:\n got: %#v\nwant: %#v", got, m)
	}
}

func TestCtrlRoundTrip(t *testing.T) {
	cases := []any{
		NewIdentity("com.wash.about", 1, "0.0.1"),
		NewIdentityAck("inst-1", 42),
		NewIdentityAck("inst-2", 0), // desktop-surface: window_id omitted
		NewError(ErrCodeBadFrame, "bad frame"),
		NewChannelOpen(7, 42),
		NewChannelOpenKind(8, 0, ChannelKindBundle),
		NewChannelOpened(7, 5),
		NewChannelOpenErr(7, ErrCodeForbidden, "no"),
		NewChannelClose(5),
		NewChannelClosed(5, "peer hung up"),
	}
	for _, c := range cases {
		c := c
		t.Run(reflect.TypeOf(c).Name(), func(t *testing.T) {
			roundtripCtrl(t, c)
		})
	}
}

func TestShellRoundTrip(t *testing.T) {
	manifest := json.RawMessage(`{"id":"com.wash.about","name":"About wash"}`)
	data := json.RawMessage(`{"action":"launch","app_id":"com.wash.about"}`)
	cases := []any{
		NewShellCatalog([]ShellCatalogApp{
			{ID: "com.wash.about", Name: "About wash", Icon: "data:,", Surface: "window", Instancing: "multi"},
			{ID: "com.wash.session", Name: "wash session", Icon: "", Surface: "desktop", Instancing: "single", Disabled: true, Reason: "test"},
		}, []ShellPanelDesc{
			{AppID: "com.wash.display", Section: "Display", Element: "wash-settings-panel-display"},
		}),
		NewShellAppDeclared("inst-1", "wash-app-about", "window", manifest),
		NewShellSessionSnapshot([]SessionWindow{
			{WindowID: 42, InstanceID: "inst-1", Element: "wash-app-about", Title: "About wash", X: 40, Y: 40, W: 480, H: 320, Z: 1, State: WindowStateNormal, Focused: true},
		}, map[string]json.RawMessage{"inst-1": json.RawMessage(`{"path":"/home"}`)}),
		NewShellSessionPatch(
			SessionPatch{Op: SessionPatchWindowUpsert, Window: &SessionWindow{WindowID: 42, InstanceID: "inst-1", Element: "wash-app-about", Title: "About wash", X: 60, Y: 60, W: 480, H: 320, Z: 2, State: WindowStateNormal, Focused: true}},
			SessionPatch{Op: SessionPatchWindowDelete, WindowID: 43},
			SessionPatch{Op: SessionPatchAppState, InstanceID: "inst-1", State: json.RawMessage(`{"path":"/etc"}`)},
		),
		NewShellWindowCloseClicked(42),
		NewShellWindowFocus(42),
		NewShellWindowMove(42, 120, 80),
		NewShellWindowResize(42, 800, 600),
		NewShellWindowState(42, WindowStateMaximized),
		NewShellAppMsgSend("inst-1", data),
		NewShellAppMsgDeliver("inst-1", data),
		NewShellNotify("inst-1", "hello", "world", NotifyLevelWarn),
		NewShellChannelBind(5, 1, ChannelKindVideo),
		NewShellChannelBindBundle(9, "inst-1", "gzip"),
		NewShellChannelUnbind(5, "eof"),
		NewShellChannelCredit(5, 65536),
		NewShellLog(LogLevelError, "wash-app-about", "boom", "Error: boom\n    at foo (...)"),
	}
	for _, c := range cases {
		c := c
		t.Run(reflect.TypeOf(c).Name(), func(t *testing.T) {
			roundtripCtrl(t, c)
		})
	}
}

func TestIdentityAckOmitsWindowIDWhenZero(t *testing.T) {
	m := NewIdentityAck("inst-1", 0)
	b, err := EncodeCtrl(m)
	if err != nil {
		t.Fatal(err)
	}
	var probe map[string]any
	if err := json.Unmarshal(b, &probe); err != nil {
		t.Fatal(err)
	}
	if _, present := probe["window_id"]; present {
		t.Fatalf("window_id should be omitted when zero: %s", b)
	}
}

func TestPeekType(t *testing.T) {
	b, _ := EncodeCtrl(NewIdentity("com.wash.about", 1, "0.0.1"))
	got, err := PeekType(b)
	if err != nil {
		t.Fatal(err)
	}
	if got != TIdentity {
		t.Fatalf("got %q want %q", got, TIdentity)
	}
}

func TestDecodeCtrlUnknownTag(t *testing.T) {
	_, err := DecodeCtrl([]byte(`{"t":"made.up"}`))
	if err == nil {
		t.Fatal("want error for unknown tag")
	}
}

func roundtripEvt(t *testing.T, m any) {
	t.Helper()
	b, err := EncodeEvt(m)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeEvt(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(got, m) {
		t.Fatalf("round-trip mismatch:\n got: %#v\nwant: %#v", got, m)
	}
}

func TestEvtRoundTrip(t *testing.T) {
	cases := []any{
		NewEvtWindowMapped(1),
		NewEvtWindowFocus(1),
		NewEvtWindowUnfocus(1),
		NewEvtWindowResize(1, 800, 600),
		NewEvtWindowState(1, WindowStateMinimized),
		NewEvtWindowCloseRequested(1),
		NewEvtShutdown(),
		NewEvtWindowSetTitle(1, "Hello"),
		NewEvtWindowConfirmClose(1, true),
		NewEvtWindowConfirmClose(1, false),
		NewEvtWindowCreate(7, WindowRoleToplevel, 0, "GIMP", 1024, 768, ""),
		NewEvtWindowCreate(8, WindowRolePopup, 42, "", 200, 120, "wash-app-display"),
		NewEvtWindowCreated(7, 42),
		NewEvtWindowCreateErr(7, ErrCodeForbidden, "windows capability not declared"),
		NewEvtWindowDestroy(42),
		NewEvtSpawnRequest("com.wash.about"),
		NewEvtSpawnOk("com.wash.about", "inst-2"),
		NewEvtSpawnErr("com.wash.about", ErrCodeForbidden, "no capability"),
		NewEvtAppRestart(9, "com.wash.display"),
		NewEvtAppRestartOk(9, "inst-3"),
		NewEvtAppRestartErr(9, ErrCodeNotFound, "com.wash.display"),
		NewEvtNotify("hello", "world", NotifyLevelInfo),
		NewEvtClipboardSet("text/plain", []byte("hi")),
		NewEvtClipboardGet(42),
		NewEvtClipboardData(42, "text/plain", []byte("hi")),
		NewEvtClipboardChanged("text/plain"),
		NewEvtAppStateSet([]byte(`{"path":"/home","expanded":["/home"]}`)),
	}
	for _, c := range cases {
		c := c
		t.Run(reflect.TypeOf(c).Name(), func(t *testing.T) {
			roundtripEvt(t, c)
		})
	}
}

// EvtAppMsg carries arbitrary data as a json.RawMessage. Round-trip
// the envelope and inspect the raw bytes — the test doesn't decode
// the inner payload because the wire layer treats it as opaque.
func TestEvtAppMsgRoundTripStringData(t *testing.T) {
	in := NewEvtAppMsg(7, "hello")
	b, err := EncodeEvt(in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeEvt(b)
	if err != nil {
		t.Fatal(err)
	}
	out, ok := got.(EvtAppMsg)
	if !ok {
		t.Fatalf("got %T", got)
	}
	if out.T != TEvtAppMsg || out.Win != 7 {
		t.Fatalf("header: %+v", out)
	}
	if string(out.Data) != `"hello"` {
		t.Fatalf("data %s, want %q", out.Data, `"hello"`)
	}
}

// Binary payloads survive the JSON envelope via base64 — encoding/json's
// default for []byte fields. End-to-end binary requires raw channels;
// app_msg.data is JSON, and base64 is the standard fallback.
func TestEvtAppMsgBinaryDataAsBase64(t *testing.T) {
	binData := []byte{0x00, 0xff, 0x01, 0x02, 0x80}
	in := NewEvtAppMsg(1, binData)
	b, err := EncodeEvt(in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeEvt(b)
	if err != nil {
		t.Fatal(err)
	}
	out := got.(EvtAppMsg)
	// Inner data is a JSON string holding base64(binData).
	want := `"` + base64.StdEncoding.EncodeToString(binData) + `"`
	if string(out.Data) != want {
		t.Fatalf("data %s, want %s", out.Data, want)
	}
}

func TestPeekEvtType(t *testing.T) {
	b, _ := EncodeEvt(NewEvtWindowMapped(1))
	got, err := PeekEvtType(b)
	if err != nil {
		t.Fatal(err)
	}
	if got != TEvtWindowMapped {
		t.Fatalf("got %q want %q", got, TEvtWindowMapped)
	}
}

func TestDecodeEvtUnknownTag(t *testing.T) {
	b, _ := EncodeEvt(map[string]any{"t": "made.up", "win": uint32(1)})
	_, err := DecodeEvt(b)
	if err == nil {
		t.Fatal("want error")
	}
}
