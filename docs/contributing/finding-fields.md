# Adding a field to FindingSpec

A contributor rule, not a plugin seam: any change to the `Finding` spec schema has to be reflected in the admission
policy that guards human writes.

The [`ValidatingAdmissionPolicy`](../operate/cli.md#permissions) has to enumerate the spec fields it freezes, because
CEL cannot express "everything except these four". A new field would otherwise become writable by anyone holding
`update`. `TestAdmissionPolicyCoversEveryFindingSpecField` in `internal/action` reflects over the Go type and fails if a
field is unaccounted for — when it fires, add the field to the frozen-fields validation in **both**
`deploy/kustomize/base/admission-policy.yaml` and `charts/patchy/templates/admission-policy.yaml`. The test checks both
renderings; a Helm install left out of step would silently ship a policy that leaves the field writable.
