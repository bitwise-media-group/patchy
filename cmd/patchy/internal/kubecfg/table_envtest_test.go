// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package kubecfg_test

import (
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/kubecfg"
	"github.com/bitwise-media-group/patchy/internal/kube"
)

// The Table request is the one code path that cannot be exercised without a
// real API server: it negotiates a content type, hand-builds query parameters,
// and decodes a wire format no fake client produces. It is also the default
// output of `patchy get`, so a break here breaks the whole command — which is
// precisely what happened when the query parameters were first built through a
// scheme that had never heard of TableOptions.

const tableNamespace = "patchy"

// startTableEnv boots envtest with the CRDs and returns a connected Env.
func startTableEnv(t *testing.T) (*kubecfg.Env, client.Client) {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; skipping table envtest")
	}

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../../deploy/kustomize/base/crds"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})

	c, err := client.New(cfg, client.Options{Scheme: kube.Scheme()})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: tableNamespace}}
	if err := c.Create(t.Context(), ns); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace: %v", err)
	}
	return &kubecfg.Env{Client: c, Config: cfg, Namespace: tableNamespace}, c
}

// seedFinding creates one finding with the given severity and status.
func seedFinding(t *testing.T, c client.Client, name string, sev v1alpha1.Level, phase v1alpha1.Phase) {
	t.Helper()
	f := &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: tableNamespace,
			Labels:    map[string]string{v1alpha1.LabelSeverity: string(sev)},
		},
		Spec: v1alpha1.FindingSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: "gh"},
			Source:         "github-code-scanning",
			Advisories:     []string{"CVE-2026-0001"},
			Title:          "Command injection",
			Severity:       sev,
		},
	}
	if err := c.Create(t.Context(), f); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	f.Status.Phase = phase
	if err := c.Status().Update(t.Context(), f); err != nil {
		t.Fatalf("seed status %s: %v", name, err)
	}
}

func TestTable(t *testing.T) {
	env, c := startTableEnv(t)
	seedFinding(t, c, "fnd-1", v1alpha1.LevelCritical, v1alpha1.PhaseQueued)
	seedFinding(t, c, "fnd-2", v1alpha1.LevelLow, v1alpha1.PhaseRemediated)

	t.Run("list", func(t *testing.T) {
		tbl, err := env.Table(t.Context(), "findings", nil, "")
		if err != nil {
			t.Fatalf("Table: %v", err)
		}
		if len(tbl.Rows) != 2 {
			t.Fatalf("got %d rows, want 2", len(tbl.Rows))
		}
	})

	// includeObject=Metadata is what makes age sorting and name matching work.
	// Without it every row is anonymous and --sort-by age silently no-ops.
	t.Run("rows carry object metadata", func(t *testing.T) {
		tbl, err := env.Table(t.Context(), "findings", nil, "")
		if err != nil {
			t.Fatalf("Table: %v", err)
		}
		for _, row := range tbl.Rows {
			meta := kubecfg.RowMeta(row)
			if meta == nil {
				t.Fatal("row carries no object metadata")
			}
			if meta.Name == "" {
				t.Error("row metadata has no name")
			}
			if meta.CreationTimestamp.IsZero() {
				t.Error("row metadata has no creation timestamp; --sort-by age cannot work")
			}
		}
	})

	t.Run("label selector narrows server-side", func(t *testing.T) {
		tbl, err := env.Table(t.Context(), "findings", nil, v1alpha1.LabelSeverity+"=critical")
		if err != nil {
			t.Fatalf("Table: %v", err)
		}
		if len(tbl.Rows) != 1 {
			t.Fatalf("got %d rows, want 1", len(tbl.Rows))
		}
		if got := kubecfg.RowName(tbl.Rows[0]); got != "fnd-1" {
			t.Errorf("selector returned %q, want fnd-1", got)
		}
	})

	t.Run("single name is a get", func(t *testing.T) {
		tbl, err := env.Table(t.Context(), "findings", []string{"fnd-2"}, "")
		if err != nil {
			t.Fatalf("Table: %v", err)
		}
		if len(tbl.Rows) != 1 || kubecfg.RowName(tbl.Rows[0]) != "fnd-2" {
			t.Errorf("got %d rows (%q), want just fnd-2", len(tbl.Rows), kubecfg.RowName(tbl.Rows[0]))
		}
	})

	t.Run("missing name is a not-found error", func(t *testing.T) {
		_, err := env.Table(t.Context(), "findings", []string{"ghost"}, "")
		if !apierrors.IsNotFound(err) {
			t.Errorf("err = %v, want NotFound so the CLI can exit 3", err)
		}
	})

	// Every noun the registry offers has to be tabulatable, or `patchy get
	// <that noun>` fails at the first request.
	t.Run("every kind tabulates", func(t *testing.T) {
		for _, plural := range []string{
			"findings", "investigations", "remediations",
			"findingrollups", "repositories", "integrations", "forges",
		} {
			if _, err := env.Table(t.Context(), plural, nil, ""); err != nil {
				t.Errorf("Table(%s): %v", plural, err)
			}
		}
	})
}

// TestTableColumns pins the contract that makes client-side column lists
// unnecessary: the server renders the CRD's own additionalPrinterColumns, and
// the priority markers that drive -o wide survive to the client.
func TestTableColumns(t *testing.T) {
	env, c := startTableEnv(t)
	seedFinding(t, c, "fnd-1", v1alpha1.LevelCritical, v1alpha1.PhaseQueued)

	tbl, err := env.Table(t.Context(), "findings", nil, "")
	if err != nil {
		t.Fatalf("Table: %v", err)
	}

	cols := make([]string, 0, len(tbl.ColumnDefinitions))
	lowPriority := 0
	for _, def := range tbl.ColumnDefinitions {
		cols = append(cols, def.Name)
		if def.Priority != 0 {
			lowPriority++
		}
	}
	for _, want := range []string{"Name", "Repo", "Severity", "Priority", "Phase", "Age"} {
		if !contains(cols, want) {
			t.Errorf("column %q missing from %v", want, cols)
		}
	}
	if lowPriority == 0 {
		t.Error("no priority>0 columns returned; -o wide would show nothing extra")
	}
}

// TestTableAcrossNamespaces covers the -A path, where no namespace is set.
func TestTableAcrossNamespaces(t *testing.T) {
	env, c := startTableEnv(t)
	seedFinding(t, c, "fnd-1", v1alpha1.LevelHigh, v1alpha1.PhaseQueued)

	all := &kubecfg.Env{Client: env.Client, Config: env.Config, Namespace: ""}
	tbl, err := all.Table(t.Context(), "findings", nil, "")
	if err != nil {
		t.Fatalf("Table: %v", err)
	}
	if len(tbl.Rows) == 0 {
		t.Error("cluster-wide table returned no rows")
	}
	if got := all.Scope(); got != "all namespaces" {
		t.Errorf("Scope() = %q", got)
	}
}

func TestConnectRejectsBadKubeconfig(t *testing.T) {
	_, err := kubecfg.Connect(kubecfg.Options{Kubeconfig: "/nonexistent/kubeconfig"})
	if err == nil {
		t.Fatal("Connect accepted a missing kubeconfig")
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
