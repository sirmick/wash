package team

import (
	"strings"
	"testing"
)

func TestParseINI(t *testing.T) {
	got, err := ParseINI(strings.NewReader("name = wash\n[server]\nPort = 8080\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got[""]["name"] != "wash" || got["server"]["port"] != "8080" {
		t.Errorf("ParseINI = %v", got)
	}
}
