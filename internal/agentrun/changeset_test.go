// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package agentrun

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/envelope"
)

// repoFixture is a scripted git repository: an initial commit (the base),
// then arbitrary mutations committed on a branch.
type repoFixture struct {
	t    *testing.T
	dir  string
	base string
}

const fixtureBranch = "patchy/issue-1"

func newRepoFixture(t *testing.T) *repoFixture {
	t.Helper()
	f := &repoFixture{t: t, dir: t.TempDir()}
	f.git("init", "-b", "main")
	f.git("config", "user.email", "test@example.com")
	f.git("config", "user.name", "test")
	f.git("config", "commit.gpgsign", "false")
	f.write("keep.txt", "keep\n")
	f.write("old.txt", "old\n")
	f.write("change.txt", "before\n")
	f.git("add", ".")
	f.git("commit", "-m", "initial")
	f.base = f.git("rev-parse", "HEAD")
	f.git("checkout", "-b", fixtureBranch)
	return f
}

func (f *repoFixture) git(args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *repoFixture) write(path, content string) {
	f.t.Helper()
	full := filepath.Join(f.dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// build runs buildChangeset over the fixture's base..branch with the given
// size cap.
func (f *repoFixture) build(maxBytes int) (*envelope.Changeset, error) {
	return buildChangeset(context.Background(), f.dir, f.base, fixtureBranch, maxBytes)
}

func TestBuildChangesetAddModifyDelete(t *testing.T) {
	f := newRepoFixture(t)
	f.write("new.txt", "new\n")
	f.write("change.txt", "after\n")
	f.git("rm", "-q", "old.txt")
	f.git("add", ".")
	f.git("commit", "-m", "fix: the works")

	cs, err := f.build(5 << 20)
	if err != nil {
		t.Fatalf("buildChangeset() error = %v", err)
	}
	if cs.BaseSHA != f.base {
		t.Errorf("BaseSHA = %q, want %q", cs.BaseSHA, f.base)
	}
	if !strings.Contains(cs.CommitMessage, "fix: the works") {
		t.Errorf("CommitMessage = %q", cs.CommitMessage)
	}
	got := map[string]string{}
	for _, up := range cs.Upserts {
		got[up.Path] = decodeB64(t, up.ContentB64)
	}
	if len(got) != 2 || got["new.txt"] != "new\n" || got["change.txt"] != "after\n" {
		t.Errorf("upserts = %v, want new.txt + change.txt contents", got)
	}
	if len(cs.Deletes) != 1 || cs.Deletes[0] != "old.txt" {
		t.Errorf("Deletes = %v, want [old.txt]", cs.Deletes)
	}
}

func TestBuildChangesetRenameBecomesDeleteAdd(t *testing.T) {
	f := newRepoFixture(t)
	f.git("mv", "old.txt", "renamed.txt")
	f.git("commit", "-m", "chore: rename")

	cs, err := f.build(5 << 20)
	if err != nil {
		t.Fatalf("buildChangeset() error = %v", err)
	}
	if len(cs.Upserts) != 1 || cs.Upserts[0].Path != "renamed.txt" {
		t.Errorf("upserts = %+v, want just renamed.txt", cs.Upserts)
	}
	if len(cs.Deletes) != 1 || cs.Deletes[0] != "old.txt" {
		t.Errorf("Deletes = %v, want [old.txt]", cs.Deletes)
	}
}

func TestBuildChangesetBinaryContent(t *testing.T) {
	f := newRepoFixture(t)
	raw := string([]byte{0x00, 0xff, 0x10, 0x0a, 0x00, 0x7f})
	f.write("blob.bin", raw)
	f.git("add", ".")
	f.git("commit", "-m", "feat: binary")

	cs, err := f.build(5 << 20)
	if err != nil {
		t.Fatalf("buildChangeset() error = %v", err)
	}
	if len(cs.Upserts) != 1 || decodeB64(t, cs.Upserts[0].ContentB64) != raw {
		t.Errorf("binary round-trip failed: %+v", cs.Upserts)
	}
}

func TestBuildChangesetExecutableMode(t *testing.T) {
	f := newRepoFixture(t)
	f.write("run.sh", "#!/bin/sh\n")
	f.git("add", ".")
	f.git("update-index", "--chmod=+x", "run.sh")
	f.git("commit", "-m", "feat: script")

	cs, err := f.build(5 << 20)
	if err != nil {
		t.Fatalf("buildChangeset() error = %v", err)
	}
	if len(cs.Upserts) != 1 || cs.Upserts[0].Mode != "100755" {
		t.Errorf("upserts = %+v, want run.sh at 100755", cs.Upserts)
	}
}

func TestBuildChangesetSymlinkMode(t *testing.T) {
	f := newRepoFixture(t)
	if err := os.Symlink("keep.txt", filepath.Join(f.dir, "link")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	f.git("add", ".")
	f.git("commit", "-m", "feat: link")

	cs, err := f.build(5 << 20)
	if err != nil {
		t.Fatalf("buildChangeset() error = %v", err)
	}
	if len(cs.Upserts) != 1 || cs.Upserts[0].Mode != "120000" {
		t.Fatalf("upserts = %+v, want link at 120000", cs.Upserts)
	}
	if got := decodeB64(t, cs.Upserts[0].ContentB64); got != "keep.txt" {
		t.Errorf("symlink target = %q, want keep.txt", got)
	}
}

func TestBuildChangesetSquashesCommitMessages(t *testing.T) {
	f := newRepoFixture(t)
	f.write("a.txt", "a\n")
	f.git("add", ".")
	f.git("commit", "-m", "fix: first")
	f.write("b.txt", "b\n")
	f.git("add", ".")
	f.git("commit", "-m", "fix: second")

	cs, err := f.build(5 << 20)
	if err != nil {
		t.Fatalf("buildChangeset() error = %v", err)
	}
	first := strings.Index(cs.CommitMessage, "fix: first")
	second := strings.Index(cs.CommitMessage, "fix: second")
	if first == -1 || second == -1 || second < first {
		t.Errorf("CommitMessage = %q, want both messages oldest-first", cs.CommitMessage)
	}
}

func TestBuildChangesetSizeCap(t *testing.T) {
	f := newRepoFixture(t)
	f.write("big.txt", strings.Repeat("x", 4096))
	f.git("add", ".")
	f.git("commit", "-m", "feat: big")

	if _, err := f.build(1024); !errors.Is(err, errChangesetTooLarge) {
		t.Errorf("error = %v, want errChangesetTooLarge", err)
	}
}

func TestBuildChangesetRejectsGitlink(t *testing.T) {
	f := newRepoFixture(t)
	// A gitlink entry (mode 160000) without the ceremony of a real
	// submodule: stage the tree entry directly.
	f.git("update-index", "--add", "--cacheinfo", "160000,"+f.base+",vendor/dep")
	f.git("commit", "-m", "feat: submodule")

	if _, err := f.build(5 << 20); err == nil || !strings.Contains(err.Error(), "unsupported mode 160000") {
		t.Errorf("error = %v, want unsupported-mode rejection", err)
	}
}

func TestBuildChangesetRejectsEmptyRange(t *testing.T) {
	f := newRepoFixture(t)
	if _, err := f.build(5 << 20); err == nil {
		t.Error("error = nil, want empty-changeset rejection")
	}
}
