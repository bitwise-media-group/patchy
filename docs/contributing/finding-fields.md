# Adding a field to FindingSpec

This is a rule for contributors. If you change the `Finding` spec schema, you must also update the admission policy that
guards human writes.

The [`ValidatingAdmissionPolicy`](../operate/cli.md#permissions) has to list every spec field it freezes, because CEL
cannot express "everything except these four". If the policy does not list a new field, anyone holding `update` can
write it. `TestAdmissionPolicyCoversEveryFindingSpecField` in `internal/action` reflects over the Go type and fails if
the policy misses a field. When it fails, add the field to the frozen-fields validation in **both**
`deploy/kustomize/base/admission-policy.yaml` and `charts/patchy/templates/admission-policy.yaml`. The test checks both
files. If the Helm copy falls out of step, a Helm install would quietly ship a policy that leaves the field writable.
