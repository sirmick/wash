package agentd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/agentpolicy"
)

// The Test button's check: the key goes as a bearer token, a good answer is
// summarised for a person, and a refusal says so.
func TestTestKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-or-good-1234" {
			http.Error(w, `{"error":{"message":"No auth credentials found"}}`, http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"label":"sk-or-v1-abc...234","usage":1.5,"limit":20}}`))
	}))
	defer srv.Close()
	ok, detail := testKey(context.Background(), srv.URL, "sk-or-good-1234")
	if !ok || detail != "valid: sk-or-v1-abc...234, $1.50 used of $20.00" {
		t.Errorf("good key: %v %q", ok, detail)
	}
	ok, detail = testKey(context.Background(), srv.URL, "sk-or-bad-9999")
	if ok || !strings.Contains(detail, "refused this key (401") || strings.Contains(detail, "9999") {
		t.Errorf("bad key: %v %q", ok, detail)
	}
	srv.Close()
	if ok, detail := testKey(context.Background(), srv.URL, "sk-or-good-1234"); ok || strings.Contains(detail, "sk-or-good") {
		t.Errorf("unreachable: %v %q", ok, detail)
	}
}

// What the roster publishes about keys: set or not and the last four
// characters, never the value; and keys named by a user's own connections.
func TestPublishKeysNeverCarriesAValue(t *testing.T) {
	pol := agentpolicy.Policy{Connections: map[string]agentpolicy.Connection{"claude@work": {Adapter: "claude", Key: "work", KeyEnv: []string{"ANTHROPIC_API_KEY"}}}}
	views := publishKeys(pol, map[string]string{"openrouter": "sk-or-v1-secret-value-wxyz"})
	b, _ := json.Marshal(views)
	if strings.Contains(string(b), "secret-value") {
		t.Fatalf("a key value was published: %s", b)
	}
	if len(views) != 2 || views[0].ID != "openrouter" || !views[0].Set || views[0].Hint != "wxyz" || !views[0].Testable {
		t.Errorf("openrouter = %+v", views)
	}
	if views[1].ID != "work" || views[1].Set || views[1].Testable {
		t.Errorf("user connection key = %+v", views[1])
	}
}
