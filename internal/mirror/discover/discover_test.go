// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package discover

import (
	"strings"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

const rendered = `---
# Source: demo/templates/deploy.yaml
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      initContainers:
        - name: init
          image: busybox:1.36
      containers:
        - name: app
          image: ghcr.io/example/app:v1.2.3
        - name: sidecar
          image: grafana/agent:v0.40.0
---
# Source: demo/templates/crd.yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
spec:
  versions:
    - schema:
        openAPIV3Schema:
          properties:
            containers:
              # a schema map, not a pod list — must be skipped
              type: array
---
# Source: demo/templates/prometheus.yaml
apiVersion: monitoring.coreos.com/v1
kind: Prometheus
spec:
  image: quay.io/prometheus/prometheus:v2.50.0
---
# Source: demo/templates/cm.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: embedded
data:
  pod.yaml: |
    spec:
      containers:
        - image: ghcr.io/embedded/from-walk:v9
    image: registry.example.com/embedded/tool:v2.0.0
  config.toml: |
    image = "docker.io/library/tool:1.0"
    templated = "image: {{ .Values.image }}:latest"
---
# Source: demo/templates/test-hook.yaml
apiVersion: v1
kind: Pod
spec:
  containers:
    - name: test
      image: ghcr.io/example/test-runner:v1
`

func TestDiscover(t *testing.T) {
	got, err := Discover(Input{
		Rendered:   []byte(rendered),
		AppVersion: "v9.9.9",
		Extra: []spec.ExtraImage{
			{Image: "quay.io/example/solver:{appVersion}", Reason: "flag-launched"},
		},
		Exclude: []spec.ExcludePattern{{Pattern: "ghcr.io/example/test-*"}},
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	want := []string{
		"docker.io/grafana/agent:v0.40.0",
		"docker.io/library/busybox:1.36",
		"docker.io/library/tool:1.0",
		"ghcr.io/embedded/from-walk:v9",
		"ghcr.io/example/app:v1.2.3",
		"quay.io/example/solver:v9.9.9",
		"quay.io/prometheus/prometheus:v2.50.0",
		"registry.example.com/embedded/tool:v2.0.0",
	}
	if strings.Join(got.Images, "\n") != strings.Join(want, "\n") {
		t.Errorf("Images:\n got %v\nwant %v", got.Images, want)
	}
	if len(got.Excluded) != 1 || got.Excluded[0] != "ghcr.io/example/test-runner:v1" {
		t.Errorf("Excluded = %v", got.Excluded)
	}
}

func TestDiscoverEmpty(t *testing.T) {
	crdOnly := "kind: CustomResourceDefinition\nspec: {}\n"
	if _, err := Discover(Input{Rendered: []byte(crdOnly)}); err == nil {
		t.Error("want error when nothing discovered and allowEmpty unset")
	}
	got, err := Discover(Input{Rendered: []byte(crdOnly), AllowEmpty: true})
	if err != nil || len(got.Images) != 0 {
		t.Errorf("allowEmpty: %v, %v", got, err)
	}
}
