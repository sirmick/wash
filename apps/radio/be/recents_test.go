package radio

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNoteGateSuppressesReconnects(t *testing.T) {
	g := newNoteGate()
	t0 := time.Unix(1_700_000_000, 0)
	if !g.allow("Groove Salad", t0) {
		t.Fatal("first play not noted")
	}
	if g.allow("Groove Salad", t0.Add(5*time.Second)) {
		t.Error("a reconnect to the same station inside the window was noted again")
	}
	if !g.allow("Drone Zone", t0.Add(6*time.Second)) {
		t.Error("a different station was suppressed")
	}
	if !g.allow("Groove Salad", t0.Add(7*time.Second)) {
		t.Error("switching back to a station was suppressed")
	}
	if !g.allow("Groove Salad", t0.Add(7*time.Second+noteRepeatWindow)) {
		t.Error("the same station past the window was suppressed")
	}
	if g.allow("", t0) {
		t.Error("an unnamed station was noted")
	}
}

func TestTunerHoldsUntilLoadedThenForwards(t *testing.T) {
	var tu tuner
	if tu.request("one") {
		t.Fatal("request before the list loaded asked to forward")
	}
	tu.request("two") // latest wins while held
	if got := tu.loaded(); got != "two" {
		t.Errorf("loaded = %q, want the held name two", got)
	}
	if got := tu.loaded(); got != "" {
		t.Errorf("second load = %q, want nothing (delivered once)", got)
	}
	if !tu.request("three") {
		t.Error("request after the list loaded did not ask to forward")
	}
	if got := tu.loaded(); got != "" {
		t.Errorf("a forwarded request was also held: %q", got)
	}
}

// Only a stream that really started is reported (and so noted in the start
// menu): a dead station's error status is a 502, with no stream_info.
func TestProxyStreamReportsOnlyA2xxStart(t *testing.T) {
	for _, tc := range []struct {
		status   int
		wantInfo bool
		wantCode int
	}{
		{http.StatusOK, true, http.StatusOK},
		{http.StatusNotFound, false, http.StatusBadGateway},
		{http.StatusServiceUnavailable, false, http.StatusBadGateway},
	} {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "audio/mpeg")
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte("abc"))
		}))
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/stream?i=0", nil)
		infos := 0
		proxyStream(rec, req, up.URL, func(string) {}, func(streamInfo) { infos++ })
		up.Close()
		if got := infos > 0; got != tc.wantInfo {
			t.Errorf("upstream %d: onInfo called=%v, want %v", tc.status, got, tc.wantInfo)
		}
		if rec.Code != tc.wantCode {
			t.Errorf("upstream %d: proxied status %d, want %d", tc.status, rec.Code, tc.wantCode)
		}
	}
}
