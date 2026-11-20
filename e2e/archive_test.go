// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package e2e

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/e2e/fakegithub"
	"github.com/bitwise-media-group/patchy/e2e/fakes3"
	"github.com/bitwise-media-group/patchy/internal/archive"
	"github.com/bitwise-media-group/patchy/internal/envelope"
	"github.com/bitwise-media-group/patchy/internal/jobs"
	"github.com/bitwise-media-group/patchy/internal/runarchive"
	"github.com/bitwise-media-group/patchy/internal/transcript"
)

const (
	archiveBucket  = "audit"
	archiveFinding = "finding-archive-1"
	archiveRun     = archiveFinding + "-rem-1"
	archivePatch   = "From 3f1c2d9e Mon Sep 17 00:00:00 2001\nSubject: [PATCH] fix: escape the sink\n\n" +
		"diff --git a/app.js b/app.js\n"
)

// useFakeAWS points the AWS SDK default chain, in this process and in every
// binary it starts, at the fake S3's credential, and away from any real
// profile on the machine.
func useFakeAWS(t *testing.T) {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", fakes3.AccessKeyID)
	t.Setenv("AWS_SECRET_ACCESS_KEY", fakes3.SecretAccessKey)
	t.Setenv("AWS_REGION", fakes3.Region)
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "none"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "none"))
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
}

// archiveArgs are the run-archive flags every archiving binary takes.
func archiveArgs(endpoint string) []string {
	return []string{
		"--archive-provider", "s3",
		"--archive-bucket", archiveBucket,
		"--archive-prefix", "e2e",
		"--archive-region", fakes3.Region,
		"--archive-endpoint", endpoint,
	}
}

// archiveTurns is the conversation the fabricated run streamed, including a
// turn far larger than the ConfigMap store would keep (2 KiB), so the test
// can tell the full record from a compacted one.
func archiveTurns() []transcript.Turn {
	return []transcript.Turn{
		{Seq: 1, Role: transcript.RoleAssistant, Kind: transcript.KindText, Text: "Escaping the sink."},
		{Seq: 2, Role: transcript.RoleUser, Kind: transcript.KindToolResult, Text: strings.Repeat("x", 8<<10)},
		{Seq: 3, Role: transcript.RoleAssistant, Kind: transcript.KindToolUse, Tool: "Edit", Path: "app.js"},
	}
}

// TestArchive drives run archival through the shipped pieces against a fake
// S3 that verifies SigV4 like the real service. Agent Jobs never run under
// envtest, so the run's collection is the real runarchive.Collector called
// directly with a fabricated Job output, exactly as the remediation
// controller calls it; everything after that (the status page's presigned
// playback, the CLI reading the bucket with the caller's credentials) is the
// real binaries.
func TestArchive(t *testing.T) {
	cl := startCluster(t)
	gh := fakegithub.New()
	t.Cleanup(gh.Close)
	cl.githubCredentials(t, gh.URL)
	s3 := fakes3.Start()
	t.Cleanup(s3.Close)
	useFakeAWS(t)

	refusesWindowPastJobTTL(t, cl, s3.URL)
	// A valid archive configuration starts, credentials and all.
	cl.controller(t, "remediation-controller",
		append(append([]string{"--job-ttl", "1h", "--archive-retry-window", "45m", "--archive-required"},
			runnerArgs...), archiveArgs(s3.URL)...)...)

	ref := collectArchivedRun(t, cl, s3)
	checkStoredObjects(t, s3, ref)

	authCfg := filepath.Join(t.TempDir(), "auth.yaml")
	if err := os.WriteFile(authCfg, []byte("mode: none\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cl.controller(t, "status-server", append([]string{"--listen-addr", listen, "--auth-config", authCfg,
		"--archive-presign-ttl", "2m"}, archiveArgs(s3.URL)...)...)
	base := "http://" + listen

	checkStatusDetail(t, base)
	checkStatusPlayback(t, base, s3)
	checkCLI(t, cl, s3.URL)
}

// refusesWindowPastJobTTL: a retry window the Job TTL would outlive is a
// startup error, since the retry re-reads a log the TTL has deleted.
func refusesWindowPastJobTTL(t *testing.T, cl *cluster, endpoint string) {
	t.Helper()
	args := append(append([]string{"serve", "--kubeconfig", cl.kubeconfig, "--namespace", namespace,
		"--health-addr", fmt.Sprintf("127.0.0.1:%d", freePort(t)),
		"--job-ttl", "1h", "--archive-retry-window", "2h"}, runnerArgs...), archiveArgs(endpoint)...)
	cmd := exec.Command(build(t, "remediation-controller"), args...)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("remediation-controller started with a retry window past the Job TTL")
	}
	if err == nil || !strings.Contains(string(out), "must be below --job-ttl") {
		t.Fatalf("remediation-controller exit = %v, output:\n%s", err, out)
	}
}

// collectArchivedRun fabricates a finished remediation and collects it
// through the real Collector and S3 client, twice, as a controller whose
// first pass crashed after the upload would. It returns the archive ref the
// run's status now carries.
func collectArchivedRun(t *testing.T, cl *cluster, s3 *fakes3.Server) *v1alpha1.ArchiveRef {
	t.Helper()
	ctx := context.Background()
	now := metav1.Now()
	fnd := fabricateFinding(t, cl, archiveFinding, v1alpha1.LevelHigh, "https://127.0.0.1/acme/shop",
		func(st *v1alpha1.FindingStatus) {
			st.Phase = v1alpha1.PhaseInReview
			st.PhaseTimes = []v1alpha1.PhaseTime{{Phase: v1alpha1.PhaseInReview, At: now}}
			st.Remediation = &v1alpha1.RemediationSummary{
				Name: archiveRun, Attempt: 1, Outcome: "ok", Success: true, CompletedAt: &now,
			}
		})
	rem := &v1alpha1.Remediation{
		ObjectMeta: metav1.ObjectMeta{
			Name: archiveRun, Namespace: namespace,
			Labels: map[string]string{v1alpha1.LabelFinding: archiveFinding},
		},
		Spec: v1alpha1.RemediationSpec{
			FindingRef:       v1alpha1.ObjectReference{Name: archiveFinding, UID: fnd.UID},
			InvestigationRef: v1alpha1.ObjectReference{Name: archiveFinding + "-inv-1"},
			RepositoryRef:    v1alpha1.LocalObjectReference{Name: archiveFinding + "-src"},
			Attempt:          1,
		},
	}
	if err := cl.client.Create(ctx, rem); err != nil {
		t.Fatal(err)
	}

	cfg := archive.Config{Provider: archive.ProviderS3, Bucket: archiveBucket, Prefix: "e2e",
		Region: fakes3.Region, Endpoint: s3.URL}
	store, err := archive.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	col := &runarchive.Collector{Client: cl.client, Archiver: &runarchive.Archiver{
		Store: store, Config: cfg, Required: true, RetryWindow: 45 * time.Minute,
	}}
	out := jobs.RunOutput{Turns: archiveTurns(), Events: []envelope.Event{{
		V: envelope.Version, Type: envelope.TypeRemediation, Finding: archiveFinding,
		Remediation: &envelope.Remediation{
			Stage:          envelope.Stage{Outcome: envelope.OutcomeOK, Harness: "claude", Model: "anthropic/claude-sonnet-5"},
			ReportMarkdown: "---\nsuccess: true\n---\n# Escaped the sink\n",
			Success:        true,
			Changeset: &envelope.Changeset{BaseSHA: "abc123", CommitMessage: "fix: escape the sink",
				Upserts: []envelope.FileChange{{Path: "app.js", Mode: "100644", ContentB64: "eA=="}},
				Patch:   archivePatch},
		},
	}}}
	run := runarchive.Run{Kind: v1alpha1.RunKindRemediation, Object: rem, Attempt: 1, Finding: fnd,
		BaseSHA: "abc123", FinishedAt: time.Now()}
	target := runarchive.Target{GVK: metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Remediation"}}

	var rec *runarchive.Record
	for pass := range 2 {
		var retry time.Duration
		rec, retry = col.Collect(ctx, run, out, target)
		if retry != 0 || rec.Archive == nil || rec.FailDetail != "" {
			t.Fatalf("collect pass %d: retry=%s rec=%+v", pass+1, retry, rec)
		}
	}
	// The second pass met every object already there: S3 answered 412 and
	// the matching digest made it a success.
	conflicts := 0
	for _, r := range s3.Requests() {
		if r.Method == http.MethodPut && r.Status == http.StatusPreconditionFailed {
			conflicts++
		}
	}
	if conflicts != len(rec.Archive.Objects) {
		t.Errorf("second pass saw %d create-only conflicts, want one per object (%d)", conflicts, len(rec.Archive.Objects))
	}

	rem.Status = v1alpha1.RemediationStatus{Phase: v1alpha1.RunComplete, Success: true,
		Stage: &v1alpha1.StageResult{Outcome: "ok", Harness: "claude"}}
	rec.Apply(rem.Status.Stage, &rem.Status.Conditions, rem.Generation)
	if err := cl.client.Status().Update(ctx, rem); err != nil {
		t.Fatal(err)
	}
	if c := meta.FindStatusCondition(rem.Status.Conditions, v1alpha1.ConditionArchived); c == nil || c.Status != "True" {
		t.Errorf("Archived condition = %+v", c)
	}
	var cm corev1.ConfigMap
	cmKey := types.NamespacedName{Namespace: namespace, Name: archiveRun + "-transcript"}
	if err := cl.client.Get(ctx, cmKey, &cm); err == nil {
		t.Error("an archived run also wrote a transcript ConfigMap")
	}
	return rec.Archive
}

// checkStoredObjects compares what reached the bucket with what the run
// recorded: the prefix carries the run's UID, every object matches its
// digest, and the manifest describes the run.
func checkStoredObjects(t *testing.T, s3 *fakes3.Server, ref *v1alpha1.ArchiveRef) {
	t.Helper()
	if !strings.HasPrefix(ref.Prefix, "e2e/"+namespace+"/"+archiveFinding+"/"+archiveRun+"/") {
		t.Errorf("prefix = %q", ref.Prefix)
	}
	for _, o := range ref.Objects {
		stored, ok := s3.Object(archiveBucket, o.Key)
		if !ok {
			t.Errorf("%s never reached the bucket", o.Key)
			continue
		}
		if archive.SHA256(stored.Body) != o.SHA256 || stored.Metadata["sha256"] != o.SHA256 {
			t.Errorf("%s: stored digest does not match the run's record", o.Name)
		}
		if strings.HasSuffix(o.Name, ".gz") && stored.ContentType != "application/gzip" {
			t.Errorf("%s content type = %q", o.Name, stored.ContentType)
		}
	}
	man, _ := s3.Object(archiveBucket, ref.Prefix+"/manifest.json")
	var m runarchive.Manifest
	if err := json.Unmarshal(man.Body, &m); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if m.Run.Name != archiveRun || m.Finding == nil || m.Finding.Name != archiveFinding || m.BaseSHA != "abc123" ||
		len(m.Objects) != len(ref.Objects)-1 {
		t.Errorf("manifest = %s", man.Body)
	}
}

// checkStatusDetail: the finding detail tells the page the run is archived.
func checkStatusDetail(t *testing.T, base string) {
	t.Helper()
	res, err := http.Get(base + "/api/findings/" + archiveFinding)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var detail struct {
		Remediation *struct {
			Transcript *struct {
				Turns    int  `json:"turns"`
				Archived bool `json:"archived"`
			} `json:"transcript"`
			ReportArchived bool `json:"reportArchived"`
			PatchAvailable bool `json:"patchAvailable"`
		} `json:"remediation"`
	}
	if err := json.NewDecoder(res.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	r := detail.Remediation
	if r == nil || r.Transcript == nil || !r.Transcript.Archived || r.Transcript.Turns != 3 ||
		!r.ReportArchived || !r.PatchAvailable {
		t.Errorf("remediation detail = %+v", r)
	}
}

// checkStatusPlayback walks the browser's path: the transcript stream says
// `archived`, the archive route presigns, and the URL alone (no cookies, no
// credentials) reads the object from the bucket.
func checkStatusPlayback(t *testing.T, base string, s3 *fakes3.Server) {
	t.Helper()
	runPath := base + "/api/findings/" + archiveFinding + "/runs/remediation/1"
	res, err := http.Get(runPath + "/transcript")
	if err != nil {
		t.Fatal(err)
	}
	stream, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if !strings.Contains(string(stream), "event: archived\n") || !strings.Contains(string(stream), "event: end\n") {
		t.Errorf("transcript stream = %q, want archived then end", stream)
	}

	gz, _ := fetchPresigned(t, runPath+"/archive/transcript")
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatalf("archived transcript is not gzip: %v", err)
	}
	var turns []transcript.Turn
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var turn transcript.Turn
		if err := json.Unmarshal(sc.Bytes(), &turn); err != nil {
			t.Fatal(err)
		}
		turns = append(turns, turn)
	}
	if len(turns) != 3 || len(turns[1].Text) != 8<<10 {
		t.Errorf("played back %d turns, want 3 uncompacted", len(turns))
	}

	patch, disposition := fetchPresigned(t, runPath+"/archive/patch")
	if string(patch) != archivePatch || !strings.HasPrefix(disposition, "attachment;") {
		t.Errorf("patch = %q, disposition %q", patch, disposition)
	}

	presigned := 0
	for _, r := range s3.Requests() {
		if r.Presigned && r.Status == http.StatusOK {
			presigned++
		}
	}
	if presigned != 2 {
		t.Errorf("bucket served %d presigned GETs, want 2", presigned)
	}
}

// fetchPresigned asks the status server for a URL, then GETs it.
func fetchPresigned(t *testing.T, route string) ([]byte, string) {
	t.Helper()
	res, err := http.Get(route)
	if err != nil {
		t.Fatal(err)
	}
	var signed struct {
		URL       string `json:"url"`
		ExpiresAt string `json:"expiresAt"`
	}
	err = json.NewDecoder(res.Body).Decode(&signed)
	_ = res.Body.Close()
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("%s: %d (%v)", route, res.StatusCode, err)
	}
	exp, err := time.Parse(time.RFC3339, signed.ExpiresAt)
	if err != nil || time.Until(exp) > 2*time.Minute+5*time.Second {
		t.Errorf("expiresAt = %s, want within the configured 2m", signed.ExpiresAt)
	}
	res, err = http.Get(signed.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("presigned GET %s: %d %s", route, res.StatusCode, body)
	}
	return body, res.Header.Get("Content-Disposition")
}

// checkCLI runs the shipped patchy CLI, which finds the bucket from the run's
// status alone and reads it with the caller's (here, the fake's) credentials.
func checkCLI(t *testing.T, cl *cluster, endpoint string) {
	t.Helper()
	patchy := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(build(t, "patchy"), append([]string{"--kubeconfig", cl.kubeconfig, "-n", namespace,
			"review", "remediation", "--finding", archiveFinding, "--attempt", "1"}, args...)...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("patchy %v: %v\n%s", args, err, stderr.String())
		}
		return string(out)
	}
	if got := patchy("--patch"); got != archivePatch {
		t.Errorf("patchy --patch = %q", got)
	}
	got := patchy("--transcript")
	if !strings.Contains(got, "Escaping the sink.") || !strings.Contains(got, "#3 tool Edit app.js") {
		t.Errorf("patchy --transcript = %q", got)
	}
	if got = patchy("--report-full", "--raw"); !strings.HasPrefix(got, "---\nsuccess: true") {
		t.Errorf("patchy --report-full --raw = %q", got)
	}
	u := strings.TrimSpace(patchy("--patch", "--print-url"))
	if !strings.HasPrefix(u, endpoint+"/"+archiveBucket+"/") {
		t.Fatalf("patchy --print-url = %q", u)
	}
	res, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || string(body) != archivePatch {
		t.Errorf("CLI-presigned GET = %d %q", res.StatusCode, body)
	}
}
