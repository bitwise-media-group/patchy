// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/kubecfg"
	"github.com/bitwise-media-group/patchy/internal/kube"
)

// The default output of `get all` is a server-rendered table per kind, and no
// fake client produces one: the rows, the columns and the -o wide priorities
// all come from the CRDs via the API server. So the whole table path — which
// is what a user actually sees — needs a real one.

// startGetEnv boots envtest with the CRDs and returns a CLI wired to it.
func startGetEnv(t *testing.T) (*Options, *bytes.Buffer, *bytes.Buffer, client.Client) {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; skipping get envtest")
	}

	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../../deploy/kustomize/base/crds"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := testEnv.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})

	c, err := client.New(cfg, client.Options{Scheme: kube.Scheme()})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
	if err := c.Create(t.Context(), ns); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace: %v", err)
	}

	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	opts := &Options{
		Out:            out,
		ErrOut:         errOut,
		Output:         "table",
		NoColor:        true,
		RequestTimeout: 30 * time.Second,
	}
	opts.WithEnv(&kubecfg.Env{Client: c, Config: cfg, Namespace: testNamespace})
	return opts, out, errOut, c
}

// TestGetAllTables is the feature's real output: one titled table per kind that
// has anything in it, and nothing at all for the kinds that do not.
func TestGetAllTables(t *testing.T) {
	opts, out, errOut, c := startGetEnv(t)

	fnd := testFinding("fnd-1", v1alpha1.PhaseQueued)
	fnd.CreationTimestamp = metav1.Time{}
	if err := c.Create(t.Context(), fnd); err != nil {
		t.Fatalf("seed finding: %v", err)
	}
	fnd.Status.Phase = v1alpha1.PhaseQueued
	if err := c.Status().Update(t.Context(), fnd); err != nil {
		t.Fatalf("seed finding status: %v", err)
	}
	forge := &v1alpha1.Forge{
		ObjectMeta: metav1.ObjectMeta{Name: "gh", Namespace: testNamespace},
		Spec: v1alpha1.ForgeSpec{
			Provider:  v1alpha1.ForgeProviderGitHub,
			SecretRef: v1alpha1.LocalSecretReference{Name: "forge-token"},
		},
	}
	if err := c.Create(t.Context(), forge); err != nil {
		t.Fatalf("seed forge: %v", err)
	}

	if err := runGet(t.Context(), opts, &getFlags{sortBy: "name"}, "all", nil); err != nil {
		t.Fatalf("get all: %v", err)
	}
	got := out.String()

	for _, want := range []string{"Findings", "fnd-1", "Queued", "Forges", "gh"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	// The five kinds with nothing in them must not print a heading; an empty
	// namespace would otherwise be seven headings and no rows.
	for _, absent := range []string{"Investigations", "Remediations", "Finding rollups", "Repositories", "Integrations"} {
		if strings.Contains(got, absent) {
			t.Errorf("empty kind %q was printed:\n%s", absent, got)
		}
	}
	// Columns come from the CRDs, so the finding table has to carry the ones
	// the Finding CRD declares rather than some client-side guess.
	for _, want := range []string{"NAME", "SEVERITY", "PHASE", "AGE"} {
		if !strings.Contains(got, want) {
			t.Errorf("column %q missing:\n%s", want, got)
		}
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", errOut.String())
	}
}

// TestGetAllTablesEmpty covers a namespace the pipeline has not touched.
func TestGetAllTablesEmpty(t *testing.T) {
	opts, out, errOut, _ := startGetEnv(t)

	if err := runGet(t.Context(), opts, &getFlags{}, "all", nil); err != nil {
		t.Fatalf("get all: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", out.String())
	}
	if !strings.Contains(errOut.String(), "No patchy resources found") {
		t.Errorf("stderr = %q, want it to say the namespace is empty", errOut.String())
	}
}
