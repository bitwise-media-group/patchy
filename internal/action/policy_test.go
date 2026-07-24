// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package action_test

import (
	"os"
	"reflect"
	"strings"
	"testing"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

// The admission policy has to enumerate FindingSpec's fields, because CEL
// cannot subtract keys from a struct: there is no way to say "everything except
// these four". Enumeration means a field added to the Go type would silently
// fall outside the policy and become editable by anyone holding update — a
// security regression with no compile error and no failing behaviour test.
//
// This test closes that gap by deriving the expected field set from the type
// itself. It needs no cluster, so it runs on every `go test`, not only where
// envtest assets exist.

// verbGatedFields may change, each behind its own custom verb.
var verbGatedFields = map[string]string{
	"suspend":  "suspend/resume",
	"approval": "approve",
	"retry":    "retry",
	"expedite": "expedite",
}

// freeFields may change with plain update and need no verb. Relationship edges
// are documented as human-writable on the type and carry no authority.
var freeFields = map[string]bool{"related": true}

func TestAdmissionPolicyCoversEveryFindingSpecField(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/kustomize/base/admission-policy.yaml")
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	policy := string(raw)

	specType := reflect.TypeOf(v1alpha1.FindingSpec{})
	for i := range specType.NumField() {
		field := specType.Field(i)
		name := jsonName(field.Tag.Get("json"))
		if name == "" || name == "-" {
			continue
		}

		t.Run(name, func(t *testing.T) {
			switch {
			case verbGatedFields[name] != "":
				// Gated fields are named in their own validation rule.
				if !strings.Contains(policy, "spec."+name) {
					t.Errorf("spec.%s is verb-gated (%s) but the policy never mentions it",
						name, verbGatedFields[name])
				}
			case freeFields[name]:
				// Deliberately absent from the frozen list. Asserting it stays
				// absent stops it being frozen by accident later.
				if strings.Contains(policy, "spec."+name+" ==") {
					t.Errorf("spec.%s is documented as human-writable but the policy freezes it", name)
				}
			default:
				// Everything else must be pinned by the frozen-fields rule.
				if !strings.Contains(policy, "old.spec.?"+name+" == object.spec.?"+name) &&
					!strings.Contains(policy, "old.spec."+name+" == object.spec."+name) {
					t.Errorf("spec.%s is new: the admission policy does not freeze it, so anyone "+
						"holding update on findings can now change it. Add it to the "+
						"frozen-fields validation in deploy/kustomize/base/admission-policy.yaml, "+
						"or to freeFields/verbGatedFields here if that is intended.", name)
				}
			}
		})
	}
}

// TestAdmissionPolicyExemptsEveryController guards the other direction: a new
// controller that writes Finding spec must be added to the exemption, or the
// pipeline stalls the first time it tries.
func TestAdmissionPolicyExemptsEveryController(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/kustomize/base/admission-policy.yaml")
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	policy := string(raw)

	accounts, err := os.ReadFile("../../deploy/kustomize/base/serviceaccount.yaml")
	if err != nil {
		t.Fatalf("read serviceaccounts: %v", err)
	}

	for _, line := range strings.Split(string(accounts), "\n") {
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "name:"))
		// The agent SA runs in the agents namespace and never touches the API.
		if !strings.HasPrefix(name, "patchy-") || name == "patchy-agent" ||
			strings.Contains(line, "app.kubernetes.io") {
			continue
		}
		subject := "system:serviceaccount:patchy:" + name
		if !strings.Contains(policy, subject) {
			t.Errorf("service account %s is not exempt from the admission policy; if it writes "+
				"Finding spec it will be denied, and if it does not, say so here", name)
		}
	}
}

// jsonName extracts the field name from a json struct tag.
func jsonName(tag string) string {
	name, _, _ := strings.Cut(tag, ",")
	return name
}
