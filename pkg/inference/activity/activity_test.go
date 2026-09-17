package activity

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sirmick/wash/pkg/inference"
)

type fake struct {
	answers []string
	calls   []inference.Request
}

func (f *fake) Generate(_ context.Context, req inference.Request) (inference.Result, error) {
	f.calls = append(f.calls, req)
	if len(f.answers) == 0 {
		return inference.Result{}, errors.New("no answer")
	}
	a := f.answers[0]
	f.answers = f.answers[1:]
	return inference.Result{Text: a, Provider: "fake", Model: "m", ElapsedMS: 5}, nil
}

var src = Source{AppID: "com.wash.term", Title: "~/wash", Kind: "pty-tail", ContentType: "text/plain", Content: "$ make test\nFAIL\tinternal/router"}

func TestGenerateParsesAFencedBrief(t *testing.T) {
	f := &fake{answers: []string{"```json\n{\"goal\":\"get the router tests green\",\"state\":\"active\",\"blockers\":[\"internal/router fails\"]}\n```"}}
	b, meta, err := Generate(context.Background(), f, src)
	if err != nil {
		t.Fatal(err)
	}
	if b.Goal != "get the router tests green" || b.State != "active" || len(b.Blockers) != 1 {
		t.Fatalf("brief=%+v", b)
	}
	if meta.Provider != "fake" || meta.Repaired {
		t.Fatalf("meta=%+v", meta)
	}
	// The observation rides as data under the fixed instructions, and the
	// purpose names the prompt version for the audit line.
	if f.calls[0].Purpose != promptVersion || !strings.Contains(f.calls[0].Input[0].Text, "make test") {
		t.Fatalf("request=%+v", f.calls[0])
	}
}

func TestGenerateRepairsOnceThenGivesUp(t *testing.T) {
	f := &fake{answers: []string{"Sure! Here is the summary: the tests fail.", "{\"goal\":\"fix tests\",\"state\":\"waiting\"}"}}
	b, meta, err := Generate(context.Background(), f, src)
	if err != nil || b.Goal != "fix tests" || !meta.Repaired || len(f.calls) != 2 {
		t.Fatalf("b=%+v meta=%+v err=%v calls=%d", b, meta, err, len(f.calls))
	}
	if !strings.Contains(f.calls[1].Instructions, "not a valid JSON object") {
		t.Fatal("repair did not say what was wrong")
	}

	g := &fake{answers: []string{"nope", "still nope"}}
	_, _, err = Generate(context.Background(), g, src)
	var bad *ErrBadResponse
	if !errors.As(err, &bad) || bad.Raw != "still nope" || len(g.calls) != 2 {
		t.Fatalf("err=%v calls=%d", err, len(g.calls))
	}
}

func TestParseValidates(t *testing.T) {
	if _, err := Parse(`{"state":"active"}`); err == nil {
		t.Fatal("a brief without a goal was accepted")
	}
	if _, err := Parse(`{"goal":"x","state":"sleeping"}`); err == nil {
		t.Fatal("an unknown state was accepted")
	}
	b, err := Parse(`prose before {"goal":"x"} prose after`)
	if err != nil || b.State != "unknown" {
		t.Fatalf("b=%+v err=%v", b, err)
	}
}
