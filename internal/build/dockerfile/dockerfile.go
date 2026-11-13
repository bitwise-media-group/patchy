// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package dockerfile

import (
	"bytes"
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/buildkit/frontend/dockerfile/shell"
)

// Source is one image a Dockerfile build pulls.
type Source struct {
	// Ref is the image reference as BuildKit would resolve it, after
	// build-arg expansion. Empty when Err is set.
	Ref string
	// Instruction names what pulls it: "FROM", "COPY --from",
	// "RUN --mount from", or "syntax" (the frontend image).
	Instruction string
	// Line is the 1-based Dockerfile line (0 for a frontend set through
	// the BUILDKIT_SYNTAX build arg).
	Line int
	// Raw is the reference as written, before expansion.
	Raw string
	// Err explains why the reference could not be resolved to an image
	// (an expansion that yields no valid reference). A caller enforcing a
	// policy must treat it as a rejection: it cannot be proven permitted.
	Err string
}

// Input is one Dockerfile and the build inputs its references expand from.
type Input struct {
	// Content is the Dockerfile.
	Content []byte
	// Args are the build args passed to buildx (they override ARG
	// defaults, as --build-arg does).
	Args map[string]string
	// Target, when set, is the stage built; stages it does not depend on
	// are still listed (BuildKit may skip them, but a policy check must
	// not depend on which stage a caller happens to pick).
	Target string
	// Platforms are the os/arch[/variant] targets; references expand once
	// per platform (TARGETARCH and friends), deduplicated.
	Platforms []string
}

// buildArgSyntax is the build arg that overrides the # syntax= directive.
const buildArgSyntax = "BUILDKIT_SYNTAX"

// Sources lists every image the Dockerfile pulls: each FROM that is not a
// prior stage or scratch, each COPY --from and RUN --mount from= naming an
// image rather than a stage, and the frontend image (# syntax= or the
// BUILDKIT_SYNTAX build arg). References are expanded the way BuildKit
// expands them — global ARGs (defaults overridden by build args) plus the
// automatic platform args for FROM, the stage's ARG and ENV for its flags —
// once per platform, deduplicated in first-seen order.
func Sources(in Input) ([]Source, error) {
	res, err := parser.Parse(bytes.NewReader(in.Content))
	if err != nil {
		return nil, fmt.Errorf("parse Dockerfile: %w", err)
	}
	lex := shell.NewLex(res.EscapeToken)
	var out []Source
	seen := map[string]bool{}
	add := func(s Source) {
		key := s.Instruction + "\x00" + s.Ref + "\x00" + s.Raw + "\x00" + s.Err
		if !seen[key] {
			seen[key] = true
			out = append(out, s)
		}
	}

	if ref, ok := in.Args[buildArgSyntax]; ok && ref != "" {
		add(Source{Ref: ref, Raw: ref, Instruction: "syntax"})
	} else if ref, _, _, ok := parser.DetectSyntax(in.Content); ok && ref != "" {
		add(Source{Ref: ref, Raw: ref, Instruction: "syntax", Line: 1})
	}

	platforms := in.Platforms
	if len(platforms) == 0 {
		platforms = []string{"linux/" + runtime.GOARCH}
	}
	for _, platform := range platforms {
		w := walker{lex: lex, args: in.Args, platform: platform}
		for _, node := range res.AST.Children {
			for _, s := range w.visit(node) {
				add(s)
			}
		}
	}
	return out, nil
}

// walker is one platform's pass over the instructions.
type walker struct {
	lex      *shell.Lex
	args     map[string]string
	platform string
	// global holds the ARGs declared before the first FROM.
	global env
	// stage holds the current stage's ARG and ENV values.
	stage env
	// stages are the names (lowercased) and count of stages seen so far.
	stages   map[string]bool
	nstages  int
	inStages bool
}

// visit handles one instruction, returning the sources it pulls.
func (w *walker) visit(node *parser.Node) []Source {
	switch strings.ToLower(node.Value) {
	case "arg":
		w.declareArgs(node)
	case "env":
		if w.inStages {
			for n := node.Next; n != nil && n.Next != nil; n = n.Next.Next {
				w.stage.set(n.Value, w.expand(n.Next.Value, w.stage))
			}
		}
	case "from":
		return w.from(node)
	case "copy":
		return w.flagSources(node, "COPY --from", func(flag string) (string, bool) {
			return strings.CutPrefix(flag, "--from=")
		})
	case "run":
		return w.flagSources(node, "RUN --mount from", mountFrom)
	}
	return nil
}

// declareArgs records ARG NAME[=default] declarations: a build arg wins
// over the default; inside a stage, a bare redeclaration of a global ARG
// inherits its value.
func (w *walker) declareArgs(node *parser.Node) {
	scope := &w.global
	if w.inStages {
		scope = &w.stage
	}
	for n := node.Next; n != nil; n = n.Next {
		name, def, hasDef := strings.Cut(n.Value, "=")
		value, ok := w.args[name]
		switch {
		case ok:
		case hasDef:
			value = w.expand(def, *scope)
		case w.inStages:
			value, ok = w.global.get(name)
			if !ok {
				continue
			}
		default:
			continue
		}
		scope.set(name, value)
	}
}

// from handles FROM [--platform=…] <image> [AS <name>].
func (w *walker) from(node *parser.Node) []Source {
	w.inStages = true
	w.stage = env{}
	if node.Next == nil {
		return nil
	}
	raw := node.Next.Value
	ref := w.expand(raw, w.fromEnv())
	var name string
	if as := node.Next.Next; as != nil && strings.EqualFold(as.Value, "as") && as.Next != nil {
		name = strings.ToLower(as.Next.Value)
	}
	var out []Source
	if !w.isStage(ref) && !strings.EqualFold(ref, "scratch") {
		out = append(out, source("FROM", node.StartLine, raw, ref))
	}
	if w.stages == nil {
		w.stages = map[string]bool{}
	}
	if name != "" {
		w.stages[name] = true
	}
	w.nstages++
	return out
}

// flagSources reads one image-naming flag per matching flag on the node.
func (w *walker) flagSources(node *parser.Node, instruction string, pick func(string) (string, bool)) []Source {
	var out []Source
	for _, flag := range node.Flags {
		raw, ok := pick(flag)
		if !ok || raw == "" {
			continue
		}
		ref := w.expand(raw, w.flagEnv())
		if w.isStage(ref) {
			continue
		}
		out = append(out, source(instruction, node.StartLine, raw, ref))
	}
	return out
}

// mountFrom extracts from= out of a --mount=<csv> flag.
func mountFrom(flag string) (string, bool) {
	spec, ok := strings.CutPrefix(flag, "--mount=")
	if !ok {
		return "", false
	}
	for field := range strings.SplitSeq(spec, ",") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(field), "from="); ok {
			return v, true
		}
	}
	return "", false
}

// isStage reports whether ref names an earlier stage, by name or index.
func (w *walker) isStage(ref string) bool {
	if w.stages[strings.ToLower(ref)] {
		return true
	}
	i, err := strconv.Atoi(ref)
	return err == nil && i >= 0 && i < w.nstages
}

// source builds a Source, flagging an expansion that is not a reference.
func source(instruction string, line int, raw, ref string) Source {
	s := Source{Instruction: instruction, Line: line, Raw: raw, Ref: ref}
	if why := malformed(ref); why != "" {
		s.Ref, s.Err = "", fmt.Sprintf("%q expands to %q, %s", raw, ref, why)
	}
	return s
}

// malformed explains why an expanded reference is not a usable image
// reference (typically an unset build arg leaving an empty segment).
func malformed(ref string) string {
	switch {
	case ref == "":
		return "an empty reference"
	case strings.ContainsAny(ref, " \t$"):
		return "which is not an image reference"
	case strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, ":") || strings.HasPrefix(ref, "@"),
		strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ":") || strings.HasSuffix(ref, "@"),
		strings.Contains(ref, "//"), strings.Contains(ref, "/:"), strings.Contains(ref, "/@"):
		return "which has an empty component (is a build arg unset?)"
	}
	return ""
}

// fromEnv is what a FROM line expands with: the global ARGs plus the
// automatic platform args.
func (w *walker) fromEnv() env {
	e := w.platformArgs()
	for _, k := range w.global.keys {
		v, _ := w.global.get(k)
		e.set(k, v)
	}
	return e
}

// flagEnv is what an in-stage flag expands with: the stage's ARG and ENV
// values over the automatic platform args.
func (w *walker) flagEnv() env {
	e := w.platformArgs()
	for _, k := range w.stage.keys {
		v, _ := w.stage.get(k)
		e.set(k, v)
	}
	return e
}

// platformArgs are BuildKit's automatic platform args for this pass.
func (w *walker) platformArgs() env {
	var e env
	setPlatform := func(prefix, platform string) {
		parts := strings.SplitN(platform, "/", 3)
		e.set(prefix+"PLATFORM", platform)
		e.set(prefix+"OS", parts[0])
		if len(parts) > 1 {
			e.set(prefix+"ARCH", parts[1])
		}
		variant := ""
		if len(parts) > 2 {
			variant = parts[2]
		}
		e.set(prefix+"VARIANT", variant)
	}
	setPlatform("TARGET", w.platform)
	setPlatform("BUILD", "linux/"+runtime.GOARCH)
	return e
}

// expand runs BuildKit's own word expansion; an expansion error leaves the
// word as written (malformed then flags it).
func (w *walker) expand(word string, e env) string {
	out, _, err := w.lex.ProcessWord(word, e)
	if err != nil {
		return word
	}
	return out
}

// env is an ordered variable set satisfying shell.EnvGetter.
type env struct {
	keys []string
	vals map[string]string
}

func (e *env) set(k, v string) {
	if e.vals == nil {
		e.vals = map[string]string{}
	}
	if _, ok := e.vals[k]; !ok {
		e.keys = append(e.keys, k)
	}
	e.vals[k] = v
}

func (e env) get(k string) (string, bool) {
	v, ok := e.vals[k]
	return v, ok
}

// Get implements shell.EnvGetter.
func (e env) Get(k string) (string, bool) { return e.get(k) }

// Keys implements shell.EnvGetter.
func (e env) Keys() []string { return slices.Clone(e.keys) }
