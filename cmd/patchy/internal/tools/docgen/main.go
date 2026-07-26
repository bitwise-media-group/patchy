// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Command docgen renders the patchy CLI reference from the cobra command tree,
// one page per command, for humans, search engines and LLMs.
//
// It sits under cmd/patchy because cmd/patchy/internal/cli is importable only
// from there, and it is deliberately not a cmd/ entrypoint: hack/build.sh
// builds everything under cmd/, and cobra/doc pulls in a markdown-to-roff
// renderer that has no business in a shipped binary's SBOM.
//
// Run it through the docs task rather than by hand:
//
//	mise run docs
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra/doc"

	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/cli"
)

// manDate pins the man page .TH date so generation is byte-for-byte
// reproducible. Cobra otherwise stamps time.Now(), churning every page's date
// header on each regeneration. Bump this on a meaningful revision of the
// reference.
var manDate = time.Date(2026, time.July, 26, 0, 0, 0, 0, time.UTC)

func main() {
	out := flag.String("out", "docs/cli", "directory to write the reference into")
	format := flag.String("format", "markdown", "output format: markdown, man, or rest")
	flag.Parse()

	if err := run(*out, *format); err != nil {
		log.Fatalf("docgen: %v", err)
	}
}

// run renders the command tree into dir in the named format.
func run(dir, format string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	// The tree is walked for its metadata alone — no command ever runs — so
	// nothing here should reach a real stream.
	root := cli.NewRoot(&cli.Options{Out: io.Discard, ErrOut: io.Discard})
	// Cobra attaches `completion` during Execute, which never runs here — so
	// ask for it explicitly, or the reference omits a command the binary has.
	root.InitDefaultCompletionCmd()
	root.DisableAutoGenTag = true // keep the output reproducible

	switch format {
	case "markdown":
		return doc.GenMarkdownTree(root, dir)
	case "man":
		header := &doc.GenManHeader{Title: strings.ToUpper(root.Name()), Section: "1", Date: &manDate}
		return doc.GenManTree(root, header, dir)
	case "rest":
		return doc.GenReSTTree(root, dir)
	default:
		return fmt.Errorf("unknown format %q (expected markdown, man, or rest)", format)
	}
}
