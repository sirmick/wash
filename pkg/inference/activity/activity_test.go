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

func TestGenerateBatchIndexesAndRepairs(t *testing.T) {
	srcs := []Source{src, {AppID: "com.wash.edit", Title: "a.go", Kind: "app-state", Content: "{}"}, {AppID: "com.wash.about", Kind: "dom", Content: "About"}}
	f := &fake{answers: []string{"```json\n[{\"index\":2,\"goal\":\"read about\",\"state\":\"idle\"},{\"index\":0,\"goal\":\"fix tests\",\"state\":\"active\"}]\n```"}}
	out, meta, err := GenerateBatch(context.Background(), f, srcs)
	if err != nil || len(out) != 3 || out[1] != nil || out[0].Goal != "fix tests" || out[2].State != "idle" {
		t.Fatalf("out=%+v meta=%+v err=%v", out, meta, err)
	}
	if len(f.calls) != 1 || f.calls[0].Purpose != batchPromptVersion || !strings.Contains(f.calls[0].Input[0].Text, `"index":1`) {
		t.Fatalf("request=%+v", f.calls)
	}
	// One source is a plain Generate.
	g := &fake{answers: []string{`{"goal":"x","state":"done"}`}}
	one, _, err := GenerateBatch(context.Background(), g, srcs[:1])
	if err != nil || len(one) != 1 || one[0].State != "done" || g.calls[0].Purpose != promptVersion {
		t.Fatalf("one=%+v err=%v", one, err)
	}
	// A bad answer is repaired once; twice bad is ErrBadResponse.
	h := &fake{answers: []string{"nope", `[{"index":0,"goal":"a"},{"index":1,"goal":"b"},{"index":2,"goal":"c"}]`}}
	out, meta, err = GenerateBatch(context.Background(), h, srcs)
	if err != nil || !meta.Repaired || out[1].Goal != "b" {
		t.Fatalf("repair: out=%+v meta=%+v err=%v", out, meta, err)
	}
	k := &fake{answers: []string{`[{"index":7,"goal":"a"}]`, `[{"index":0,"goal":"a"},{"index":0,"goal":"b"}]`}}
	_, _, err = GenerateBatch(context.Background(), k, srcs)
	var bad *ErrBadResponse
	if !errors.As(err, &bad) {
		t.Fatalf("err=%v", err)
	}
}
