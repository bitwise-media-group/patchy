// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package jobs

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

func TestBuildSecretCarriesSecretHandoff(t *testing.T) {
	locs := []source.Location{{Path: "config.yaml", StartLine: 2, EndLine: 2, StartColumn: 10, EndColumn: 46}}
	tests := []struct {
		name    string
		secret  bool
		locs    []source.Location
		want    string
		present bool
	}{
		{"ordinary finding", false, nil, "", false},
		{"secret with locations", true, locs,
			`[{"path":"config.yaml","start_line":2,"end_line":2,"start_column":10,"end_column":46}]`, true},
		// A secret with no file location still switches the pod into secret
		// mode, so the key is present even when empty.
		{"secret without locations", true, nil, `[]`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := testSpec()
			spec.Secret, spec.SecretLocations = tt.secret, tt.locs
			s, err := buildSecret("job", "ns", spec)
			if err != nil {
				t.Fatalf("buildSecret() error = %v", err)
			}
			raw, ok := s.Data[secretKeySecrets]
			if ok != tt.present || string(raw) != tt.want {
				t.Errorf("secrets.json = %q (present %v), want %q (present %v)", raw, ok, tt.want, tt.present)
			}
		})
	}
}

// The init container copies the handoff into the workspace when present.
func TestPrepareScriptCopiesSecretHandoff(t *testing.T) {
	if !strings.Contains(prepareScript, "/patchy/input/secrets.json /workspace/input/secrets.json") {
		t.Error("prepare script does not copy secrets.json into the workspace")
	}
}

func TestSecretLocations(t *testing.T) {
	fnd := &v1alpha1.Finding{Spec: v1alpha1.FindingSpec{
		Category: v1alpha1.FindingCategorySecret,
		Alerts: []v1alpha1.Alert{
			{ID: "1", Locations: []v1alpha1.Location{
				{Path: "a.env", StartLine: 1, EndLine: 1, StartColumn: 5, EndColumn: 20},
				{Path: "a.env", StartLine: 1, EndLine: 1, StartColumn: 5, EndColumn: 20},
			}},
			{ID: "2", Locations: []v1alpha1.Location{{Path: "b.env", StartLine: 3}}},
		},
	}}
	want := []source.Location{
		{Path: "a.env", StartLine: 1, EndLine: 1, StartColumn: 5, EndColumn: 20},
		{Path: "b.env", StartLine: 3},
	}
	if got := SecretLocations(fnd); !reflect.DeepEqual(got, want) {
		t.Errorf("SecretLocations() = %+v, want %+v", got, want)
	}
	fnd.Spec.Category = ""
	if got := SecretLocations(fnd); got != nil {
		t.Errorf("SecretLocations(ordinary) = %+v, want nil", got)
	}
	// The handoff's wire shape is pkg/source's public JSON contract.
	raw, _ := json.Marshal(want[:1])
	if !strings.Contains(string(raw), `"start_column":5`) {
		t.Errorf("location JSON = %s, want snake_case columns", raw)
	}
}
