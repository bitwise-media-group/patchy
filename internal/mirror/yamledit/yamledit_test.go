// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package yamledit

import (
	"strings"
	"testing"
)

// valuesFixture mirrors a real discovery values file: comments, tracked
// pins, nested sequences. Every byte outside the edited token must survive.
const valuesFixture = `# Copyright 2026 Bitwise Media Group Ltd.
# SPDX-License-Identifier: MIT

# The pinned line below is FACT — never hand-edit it.
template:
  spec:
    containers:
      - name: runner # the only container
        image: ghcr.io/actions/actions-runner:2.336.0
        command: ["/home/runner/run.sh"]
extra:
  quoted: "0.165.0"
  single: 'v1.2.3'
  weird."key": true
`

func TestGet(t *testing.T) {
	tests := []struct {
		path, want string
	}{
		{".template.spec.containers[0].image", "ghcr.io/actions/actions-runner:2.336.0"},
		{".template.spec.containers[0].name", "runner"},
		{".extra.quoted", "0.165.0"},
		{".extra.single", "v1.2.3"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, err := Get([]byte(valuesFixture), tt.path)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got != tt.want {
				t.Errorf("Get(%s) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestGetErrors(t *testing.T) {
	for _, path := range []string{
		".missing", ".template.spec.containers[9].image", ".template.spec.containers",
		"template", ".template.spec.containers[0].image.deeper", "",
	} {
		if _, err := Get([]byte(valuesFixture), path); err == nil {
			t.Errorf("Get(%q): want error", path)
		}
	}
}

func TestSetPlain(t *testing.T) {
	got, err := Set([]byte(valuesFixture),
		".template.spec.containers[0].image",
		"ghcr.io/actions/actions-runner:2.336.0",
		"ghcr.io/actions/actions-runner:2.337.1")
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	want := strings.Replace(valuesFixture,
		"ghcr.io/actions/actions-runner:2.336.0",
		"ghcr.io/actions/actions-runner:2.337.1", 1)
	if string(got) != want {
		t.Errorf("Set result:\n%s\nwant:\n%s", got, want)
	}
}

func TestSetPreservesQuoting(t *testing.T) {
	t.Run("double", func(t *testing.T) {
		got, err := Set([]byte(valuesFixture), ".extra.quoted", "0.165.0", "0.166.0")
		if err != nil {
			t.Fatalf("Set: %v", err)
		}
		if !strings.Contains(string(got), `quoted: "0.166.0"`) {
			t.Errorf("double-quote style lost:\n%s", got)
		}
	})
	t.Run("single", func(t *testing.T) {
		got, err := Set([]byte(valuesFixture), ".extra.single", "v1.2.3", "v1.3.0")
		if err != nil {
			t.Fatalf("Set: %v", err)
		}
		if !strings.Contains(string(got), `single: 'v1.3.0'`) {
			t.Errorf("single-quote style lost:\n%s", got)
		}
	})
}

func TestSetWholeFileByteIdentity(t *testing.T) {
	// Everything but the token — comments, the trailing comment on the
	// name line, indentation — must be byte-identical.
	got, err := Set([]byte(valuesFixture), ".extra.quoted", "0.165.0", "9.9.9")
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	want := strings.Replace(valuesFixture, `"0.165.0"`, `"9.9.9"`, 1)
	if string(got) != want {
		t.Errorf("byte identity broken:\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

func TestSetVerifiesOldValue(t *testing.T) {
	_, err := Set([]byte(valuesFixture), ".extra.quoted", "wrong", "0.166.0")
	if err == nil || !strings.Contains(err.Error(), `expected "wrong"`) {
		t.Errorf("want old-value mismatch error, got %v", err)
	}
}

func TestSetQuotedKeyPath(t *testing.T) {
	src := "a:\n  \"k.ey\": v1\n"
	got, err := Set([]byte(src), `.a."k.ey"`, "v1", "v2")
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if string(got) != "a:\n  \"k.ey\": v2\n" {
		t.Errorf("got %q", got)
	}
}

func TestSetManifestVersion(t *testing.T) {
	// The upgrade splice: a chart manifest with comments everywhere.
	src := `apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1
kind: Chart
name: demo
chart:
  repo: oci://ghcr.io/example/charts
  name: demo
  # open-telemetry publishes the OCI charts unsigned — documented gap.
  version: "0.165.0" # pinned
  versionConstraint: ">=0.130.0 <1.0.0"
`
	got, err := Set([]byte(src), ".chart.version", "0.165.0", "0.169.0")
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	want := strings.Replace(src, `"0.165.0"`, `"0.169.0"`, 1)
	if string(got) != want {
		t.Errorf("manifest splice:\n%s", got)
	}
}

func TestSetFlowSequence(t *testing.T) {
	src := "list: [a, b, c]\n"
	got, err := Set([]byte(src), ".list[1]", "b", "bee")
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if string(got) != "list: [a, bee, c]\n" {
		t.Errorf("got %q", got)
	}
}

func TestSetQuotesUnsafeReplacement(t *testing.T) {
	src := "v: plain\n"
	got, err := Set([]byte(src), ".v", "plain", "has space")
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if string(got) != "v: \"has space\"\n" {
		t.Errorf("got %q", got)
	}
}

func TestSetRejectsBlockScalar(t *testing.T) {
	src := "v: |\n  line1\n  line2\n"
	if _, err := Set([]byte(src), ".v", "line1\nline2\n", "x"); err == nil {
		t.Error("want block-scalar rejection")
	}
}

func TestSetAnchorAlias(t *testing.T) {
	src := "a: &x v1\nb: *x\n"
	// Editing through the alias resolves to the anchor's node.
	got, err := Set([]byte(src), ".a", "v1", "v2")
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if string(got) != "a: &x v2\nb: *x\n" {
		t.Errorf("got %q", got)
	}
}
