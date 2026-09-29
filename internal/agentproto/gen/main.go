// Command gen writes what is generated from internal/agentproto: the
// TypeScript types frontends use, and the message reference in
// docs/AGENT_PROTOCOL.md (between its GENERATED markers).
//
//	go run ./internal/agentproto/gen -ts web/lib/src/agent-protocol.gen.ts -doc docs/AGENT_PROTOCOL.md
//
// Run from the repository root (make gen-agent-protocol). Field and type
// comments are read from the Go source, so the generated files say what the
// structs say.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/sirmick/wash/internal/agentproto"
)

const modulePath = "github.com/sirmick/wash/"

func main() {
	tsOut := flag.String("ts", "", "write the TypeScript here")
	docOut := flag.String("doc", "", "rewrite the generated section of this Markdown file")
	flag.Parse()
	g := newGen()
	for _, m := range agentproto.Messages {
		g.collect(reflect.TypeOf(m.Payload))
	}
	if *tsOut != "" {
		must(os.WriteFile(*tsOut, g.typescript(), 0o644))
	}
	if *docOut != "" {
		old, err := os.ReadFile(*docOut)
		must(err)
		must(os.WriteFile(*docOut, spliceDoc(old, g.markdown()), 0o644))
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentproto gen:", err)
		os.Exit(1)
	}
}

type gen struct {
	// named is every named struct reachable from a message, by TS name.
	named map[string]reflect.Type
	order []string
	// docs holds Go comments: "Type" and "Type.Field".
	docs   map[string]string
	parsed map[string]bool
}

func newGen() *gen {
	return &gen{named: map[string]reflect.Type{}, docs: map[string]string{}, parsed: map[string]bool{}}
}

func (g *gen) collect(t reflect.Type) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Map || t.Kind() == reflect.Array {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || t.Name() == "" {
		return
	}
	if _, seen := g.named[t.Name()]; seen {
		if g.named[t.Name()] != t {
			must(fmt.Errorf("two types named %s (%s, %s)", t.Name(), g.named[t.Name()].PkgPath(), t.PkgPath()))
		}
		return
	}
	g.named[t.Name()] = t
	g.order = append(g.order, t.Name())
	g.parse(t.PkgPath())
	for _, f := range fields(t) {
		g.collect(f.typ)
	}
}

// parse reads a package's source for its comments.
func (g *gen) parse(pkg string) {
	if g.parsed[pkg] || !strings.HasPrefix(pkg, modulePath) {
		return
	}
	g.parsed[pkg] = true
	dir := strings.TrimPrefix(pkg, modulePath)
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, parser.ParseComments)
	must(err)
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, decl := range f.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.TYPE {
					continue
				}
				for _, spec := range gd.Specs {
					ts := spec.(*ast.TypeSpec)
					c := ts.Doc
					if c == nil && len(gd.Specs) == 1 {
						c = gd.Doc
					}
					g.docs[ts.Name.Name] = text(c)
					st, ok := ts.Type.(*ast.StructType)
					if !ok {
						continue
					}
					for _, fl := range st.Fields.List {
						for _, n := range fl.Names {
							g.docs[ts.Name.Name+"."+n.Name] = text(fl.Doc)
						}
					}
				}
			}
		}
	}
}

func text(c *ast.CommentGroup) string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.Text())
}

type field struct {
	goName   string
	jsonName string
	typ      reflect.Type
	optional bool
	owner    string
}

// fields is t's JSON fields in declaration order, with embedded structs
// flattened as encoding/json does.
func fields(t reflect.Type) []field {
	var out []field
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() && !f.Anonymous {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			et := f.Type
			if et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				out = append(out, fields(et)...)
				continue
			}
		}
		if name == "" {
			name = f.Name
		}
		// omitzero as well as omitempty: both leave the field off the wire,
		// so both make it optional in TypeScript. Reading omitempty alone
		// declared swarm.Workspace.Supervisor always present when a
		// default-configured workspace omits it entirely.
		optional := strings.Contains(opts, "omitempty") || strings.Contains(opts, "omitzero")
		out = append(out, field{goName: f.Name, jsonName: name, typ: f.Type, optional: optional, owner: t.Name()})
	}
	return out
}

var rawMessage = reflect.TypeOf(json.RawMessage(nil))

// ts renders a Go type as TypeScript.
func (g *gen) ts(t reflect.Type) string {
	if t == rawMessage {
		return "unknown"
	}
	switch t.Kind() {
	case reflect.Pointer:
		return g.ts(t.Elem())
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		inner := g.ts(t.Elem())
		if strings.ContainsAny(inner, " |") {
			inner = "(" + inner + ")"
		}
		return inner + "[]"
	case reflect.Map:
		return "Record<" + g.ts(t.Key()) + ", " + g.ts(t.Elem()) + ">"
	case reflect.Interface:
		return "unknown"
	case reflect.Struct:
		if t.Name() == "" {
			return "Record<string, never>"
		}
		return t.Name()
	}
	must(fmt.Errorf("no TypeScript for %s", t))
	return ""
}

// nullable is whether encoding/json can write null for a field that is
// not omitempty.
func nullable(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Interface:
		return t != rawMessage
	}
	return false
}

func (g *gen) fieldTS(f field) (name, typ string) {
	typ = g.ts(f.typ)
	name = f.jsonName
	if f.optional {
		name += "?"
	} else if nullable(f.typ) {
		typ += " | null"
	}
	return name, typ
}

func jsdoc(b *bytes.Buffer, indent, s string) {
	if s == "" {
		return
	}
	lines := strings.Split(s, "\n")
	if len(lines) == 1 {
		fmt.Fprintf(b, "%s/** %s */\n", indent, strings.ReplaceAll(s, "*/", "*\\/"))
		return
	}
	fmt.Fprintf(b, "%s/**\n", indent)
	for _, l := range lines {
		fmt.Fprintf(b, "%s * %s\n", indent, strings.TrimRight(strings.ReplaceAll(l, "*/", "*\\/"), " "))
	}
	fmt.Fprintf(b, "%s */\n", indent)
}

func (g *gen) typescript() []byte {
	var b bytes.Buffer
	b.WriteString("// Code generated by `make gen-agent-protocol` from internal/agentproto; DO NOT EDIT.\n")
	b.WriteString("//\n// The protocol between com.wash.agentd and its frontends (docs/AGENT_PROTOCOL.md).\n\n")
	fmt.Fprintf(&b, "/** The protocol version agentd sends on every State (agentproto.Version). */\nexport const AGENT_PROTOCOL_VERSION = %d;\n", agentproto.Version)
	names := append([]string(nil), g.order...)
	sort.Strings(names)
	for _, n := range names {
		t := g.named[n]
		b.WriteString("\n")
		jsdoc(&b, "", g.docs[n])
		fmt.Fprintf(&b, "export interface %s {\n", n)
		for _, m := range agentproto.Messages {
			if reflect.TypeOf(m.Payload) == t {
				fmt.Fprintf(&b, "  kind: '%s';\n", m.Kind)
				break
			}
		}
		for _, f := range fields(t) {
			jsdoc(&b, "  ", g.docs[f.owner+"."+f.goName])
			name, typ := g.fieldTS(f)
			fmt.Fprintf(&b, "  %s: %s;\n", name, typ)
		}
		b.WriteString("}\n")
	}
	for _, dir := range []agentproto.Dir{agentproto.Request, agentproto.Push, agentproto.DesktopDir} {
		union := map[agentproto.Dir]string{agentproto.Request: "AgentdRequest", agentproto.Push: "AgentdPush", agentproto.DesktopDir: "DesktopEvent"}[dir]
		var members []string
		for _, m := range agentproto.Messages {
			if m.Dir == dir {
				members = append(members, reflect.TypeOf(m.Payload).Name())
			}
		}
		fmt.Fprintf(&b, "\n/** Every %s, discriminated by kind. */\nexport type %s =\n  | %s;\n", dir, union, strings.Join(members, "\n  | "))
		fmt.Fprintf(&b, "export type %sKind = %s['kind'];\n", union, union)
	}
	return b.Bytes()
}

const (
	beginMark = "<!-- BEGIN GENERATED: make gen-agent-protocol -->"
	endMark   = "<!-- END GENERATED -->"
)

func spliceDoc(old, section []byte) []byte {
	i, j := bytes.Index(old, []byte(beginMark)), bytes.Index(old, []byte(endMark))
	if i < 0 || j < i {
		must(fmt.Errorf("the document has no %q … %q section", beginMark, endMark))
	}
	var b bytes.Buffer
	b.Write(old[:i+len(beginMark)])
	b.WriteString("\n")
	b.Write(section)
	b.Write(old[j:])
	return b.Bytes()
}

// oneLine flattens text for a Markdown table cell, where a newline ends the
// row and a bare pipe starts a new column.
func oneLine(s string) string {
	return strings.ReplaceAll(strings.Join(strings.Fields(s), " "), "|", "\\|")
}

func (g *gen) markdown() []byte {
	var b bytes.Buffer
	for _, dir := range []agentproto.Dir{agentproto.Request, agentproto.Push, agentproto.DesktopDir} {
		title := map[agentproto.Dir]string{agentproto.Request: "Requests (to agentd)", agentproto.Push: "Pushes (from agentd)", agentproto.DesktopDir: "Desktop events (agentd to the desktop)"}[dir]
		fmt.Fprintf(&b, "\n### %s\n\n", title)
		who := map[agentproto.Dir]string{agentproto.Request: "From", agentproto.Push: "To", agentproto.DesktopDir: "Handled by"}[dir]
		fmt.Fprintf(&b, "| Kind | Payload | %s | Reply / class | What it does |\n|---|---|---|---|---|\n", who)
		for _, m := range agentproto.Messages {
			if m.Dir != dir {
				continue
			}
			extra := m.Reply
			if dir != agentproto.Request {
				extra = string(m.Class)
				if extra == "" {
					extra = string(agentproto.Interactive)
				}
				if m.Keyed {
					extra += ", keyed"
				}
			}
			fmt.Fprintf(&b, "| `%s` | [`%s`](#%s) | %s | %s | %s |\n", m.Kind, reflect.TypeOf(m.Payload).Name(),
				strings.ToLower(reflect.TypeOf(m.Payload).Name()), oneLine(m.From), oneLine(extra), oneLine(m.Doc))
		}
	}
	b.WriteString("\n### Types\n\nEach payload's fields, with their TypeScript type. `?` marks a field that\nmay be absent; `| null` one that may be null.\n")
	names := append([]string(nil), g.order...)
	sort.Strings(names)
	for _, n := range names {
		t := g.named[n]
		fmt.Fprintf(&b, "\n#### %s\n\n", n)
		if d := g.docs[n]; d != "" {
			fmt.Fprintf(&b, "%s\n\n", oneLine(doc.Synopsis(d)))
		}
		fs := fields(t)
		if len(fs) == 0 {
			b.WriteString("No fields.\n")
			continue
		}
		b.WriteString("| Field | Type | |\n|---|---|---|\n")
		for _, f := range fs {
			name, typ := g.fieldTS(f)
			fmt.Fprintf(&b, "| `%s` | `%s` | %s |\n", name, strings.ReplaceAll(typ, "|", "\\|"), oneLine(g.docs[f.owner+"."+f.goName]))
		}
	}
	b.WriteString("\n")
	return b.Bytes()
}
