// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/archive"
	"github.com/bitwise-media-group/patchy/internal/archive/archivetest"
	"github.com/bitwise-media-group/patchy/internal/transcript"
	"github.com/bitwise-media-group/patchy/internal/transcriptstore"
)

func cliTurns() []transcript.Turn {
	return []transcript.Turn{
		{Seq: 1, Role: transcript.RoleAssistant, Kind: transcript.KindText, Text: "Escaping the sink."},
		{Seq: 2, Role: transcript.RoleAssistant, Kind: transcript.KindToolUse, Tool: "Edit", Path: "app.js"},
	}
}

const cliPatch = "From abc Mon Sep 17 00:00:00 2001\nSubject: [PATCH] fix\n"

// archivedRun is a remediation archived to the store, plus its finding.
func archivedRun(t *testing.T, store *archivetest.Store) []client.Object {
	t.Helper()
	gz, err := transcriptstore.Marshal(cliTurns())
	if err != nil {
		t.Fatal(err)
	}
	prefix := "audit/patchy/fnd-1/fnd-1-rem-1/uid"
	ref := &v1alpha1.ArchiveRef{Provider: "s3", Bucket: "audit", Prefix: prefix}
	for name, body := range map[string][]byte{
		"transcript.jsonl.gz": gz,
		"patch.mbox":          []byte(cliPatch),
		"report.md":           []byte("---\nconfidence: 0.9\n---\n# Full report\n"),
	} {
		key := prefix + "/" + name
		if err := store.Put(context.Background(), key, body, ""); err != nil {
			t.Fatal(err)
		}
		ref.Objects = append(ref.Objects, v1alpha1.ArchiveObject{Name: name, Key: key, SHA256: archive.SHA256(body)})
	}
	rem := &v1alpha1.Remediation{
		ObjectMeta: metav1.ObjectMeta{Name: "fnd-1-rem-1", Namespace: testNamespace},
		Spec:       v1alpha1.RemediationSpec{FindingRef: v1alpha1.ObjectReference{Name: "fnd-1"}, Attempt: 1},
		Status: v1alpha1.RemediationStatus{
			Phase: v1alpha1.RunComplete,
			Stage: &v1alpha1.StageResult{
				Outcome:    "ok",
				Transcript: &v1alpha1.TranscriptRef{Object: "transcript.jsonl.gz", Turns: 2},
				Archive:    ref,
			},
		},
	}
	return []client.Object{testFinding("fnd-1", v1alpha1.PhaseInReview), rem}
}

// configMapRun is an investigation whose transcript is a ConfigMap.
func configMapRun(t *testing.T) []client.Object {
	t.Helper()
	gz, err := transcriptstore.Marshal(cliTurns())
	if err != nil {
		t.Fatal(err)
	}
	return []client.Object{
		testFinding("fnd-1", v1alpha1.PhaseQueued),
		&v1alpha1.Investigation{
			ObjectMeta: metav1.ObjectMeta{Name: "fnd-1-inv-1", Namespace: testNamespace},
			Spec:       v1alpha1.InvestigationSpec{FindingRef: v1alpha1.ObjectReference{Name: "fnd-1"}, Attempt: 1},
			Status: v1alpha1.InvestigationStatus{
				Phase: v1alpha1.RunComplete,
				Stage: &v1alpha1.StageResult{
					Outcome:    "ok",
					Transcript: &v1alpha1.TranscriptRef{Name: "fnd-1-inv-1-transcript", Turns: 2},
				},
			},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "fnd-1-inv-1-transcript", Namespace: testNamespace},
			BinaryData: map[string][]byte{transcriptstore.DataKey: gz},
		},
	}
}

func (h *harness) exec(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := NewRoot(h.opts)
	root.SetOut(h.out)
	root.SetErr(h.errOut)
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return h.out.String(), err
}

func TestReviewArchivedArtifacts(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    []string
		notWant string
	}{
		{name: "transcript rendered", args: []string{"--transcript"},
			want: []string{"#1 assistant", "    Escaping the sink.", "#2 tool Edit app.js"}},
		{name: "transcript raw", args: []string{"--transcript", "--raw"},
			want: []string{`"seq":1`, `"tool":"Edit"`}, notWant: "#1"},
		{name: "patch", args: []string{"--patch"}, want: []string{cliPatch}},
		{name: "full report", args: []string{"--report-full"}, want: []string{"# Full report"}, notWant: "confidence"},
		{name: "full report raw", args: []string{"--report-full", "--raw"}, want: []string{"confidence: 0.9"}},
		{name: "presigned patch", args: []string{"--patch", "--print-url"},
			want: []string{"https://archive.test/audit/patchy/fnd-1/fnd-1-rem-1/uid/patch.mbox"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := archivetest.New()
			h := newHarness(t, archivedRun(t, store)...)
			h.opts.WithArchive(func(_ context.Context, cfg archive.Config) (archive.Store, error) {
				if cfg.Bucket != "audit" || cfg.Provider != archive.ProviderS3 {
					t.Errorf("opened %+v, want the run's own bucket", cfg)
				}
				return store, nil
			})
			args := append([]string{"review", "remediation", "--finding", "fnd-1", "--attempt", "1"}, tt.args...)
			out, err := h.exec(t, args...)
			if err != nil {
				t.Fatalf("review: %v (%s)", err, h.errOut)
			}
			for _, w := range tt.want {
				if !strings.Contains(out, w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
			if tt.notWant != "" && strings.Contains(out, tt.notWant) {
				t.Errorf("output has %q:\n%s", tt.notWant, out)
			}
		})
	}
}

func TestReviewTranscriptFromConfigMap(t *testing.T) {
	h := newHarness(t, configMapRun(t)...)
	h.opts.WithArchive(func(context.Context, archive.Config) (archive.Store, error) {
		t.Error("a ConfigMap transcript opened the archive")
		return nil, nil
	})
	out, err := h.exec(t, "review", "investigation", "--finding", "fnd-1", "--attempt", "1", "--transcript")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if !strings.Contains(out, "Escaping the sink.") {
		t.Errorf("output = %q", out)
	}
}

func TestReviewArchiveRefusals(t *testing.T) {
	tests := []struct {
		name string
		objs func(t *testing.T, s *archivetest.Store) []client.Object
		args []string
		want string
	}{
		{name: "patch of an investigation", objs: func(t *testing.T, _ *archivetest.Store) []client.Object {
			return configMapRun(t)
		}, args: []string{"review", "investigation", "--finding", "fnd-1", "--attempt", "1", "--patch"},
			want: "--patch reads a remediation"},
		{name: "artifact of a finding", objs: func(t *testing.T, s *archivetest.Store) []client.Object {
			return archivedRun(t, s)
		}, args: []string{"review", "finding", "fnd-1", "--transcript"},
			want: "review an investigation or remediation"},
		{name: "not archived", objs: func(t *testing.T, _ *archivetest.Store) []client.Object {
			return configMapRun(t)
		}, args: []string{"review", "investigation", "--finding", "fnd-1", "--attempt", "1", "--report-full"},
			want: "was not archived"},
		{name: "url for a ConfigMap transcript", objs: func(t *testing.T, _ *archivetest.Store) []client.Object {
			return configMapRun(t)
		}, args: []string{"review", "investigation", "--finding", "fnd-1", "--attempt", "1", "--transcript", "--print-url"},
			want: "not archived"},
		{name: "tampered object", objs: func(t *testing.T, s *archivetest.Store) []client.Object {
			objs := archivedRun(t, s)
			st := objs[1].(*v1alpha1.Remediation).Status.Stage
			for i := range st.Archive.Objects {
				st.Archive.Objects[i].SHA256 = strings.Repeat("0", 64)
			}
			return objs
		}, args: []string{"review", "remediation", "--finding", "fnd-1", "--attempt", "1", "--patch"},
			want: "does not match the digest"},
		{name: "with --web", objs: func(t *testing.T, s *archivetest.Store) []client.Object {
			return archivedRun(t, s)
		}, args: []string{"review", "remediation", "--finding", "fnd-1", "--attempt", "1", "--patch", "--web"},
			want: "none of the others"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := archivetest.New()
			h := newHarness(t, tt.objs(t, store)...)
			h.opts.WithArchive(func(context.Context, archive.Config) (archive.Store, error) { return store, nil })
			_, err := h.exec(t, tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}
