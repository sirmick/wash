package wire

import (
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
		NewAssetRead(17, "index.js"),
		NewAssetReadOK(17, 12345, "application/javascript"),
		NewAssetData(17, "aGVsbG8=", false),
		NewAssetData(17, "d29ybGQ=", true),
		NewAssetReadErr(17, ErrCodeNotFound, "no such asset"),
		NewError(ErrCodeBadFrame, "bad frame"),
		NewChannelOpen(7, 42),
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
		}),
		NewShellAppDeclared("inst-1", "wash-app-about", "window", manifest),
		NewShellWindowCreate(42, "inst-1", "About wash", 480, 320),
		NewShellWindowDestroy(42),
		NewShellWindowTitle(42, "About wash"),
		NewShellAssetDeliver("inst-1", "index.js", "aGVsbG8=", true, "application/javascript"),
		NewShellAssetFetch("inst-1", "index.js"),
		NewShellWindowCloseClicked(42),
		NewShellWindowFocus(42),
		NewShellWindowResize(42, 800, 600),
		NewShellWindowState(42, WindowStateMaximized),
		NewShellAppMsgSend("inst-1", data),
		NewShellAppMsgDeliver("inst-1", data),
		NewShellNotify("inst-1", "hello", "world", NotifyLevelWarn),
		NewShellChannelBind(5, 1),
		NewShellChannelUnbind(5, "eof"),
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
		NewEvtSpawnRequest("com.wash.about"),
		NewEvtSpawnOk("com.wash.about", "inst-2"),
		NewEvtSpawnErr("com.wash.about", ErrCodeForbidden, "no capability"),
		NewEvtNotify("hello", "world", NotifyLevelInfo),
	}
	for _, c := range cases {
		c := c
		t.Run(reflect.TypeOf(c).Name(), func(t *testing.T) {
			roundtripEvt(t, c)
		})
	}
}

// EvtAppMsg carries arbitrary data. CBOR's default decoding promotes
// maps to map[interface{}]interface{} with int keys for small ints —
// so the round-trip comparison is on a representative subset rather
// than every shape.
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
	if s, _ := out.Data.(string); s != "hello" {
		t.Fatalf("data %#v, want string \"hello\"", out.Data)
	}
}

func TestEvtAppMsgPreservesBinaryData(t *testing.T) {
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
	gotBytes, ok := out.Data.([]byte)
	if !ok {
		t.Fatalf("data type %T, want []byte", out.Data)
	}
	if !reflect.DeepEqual(gotBytes, binData) {
		t.Fatalf("binary mismatch")
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
