#!/usr/bin/env sh
# Copyright 2026 Bitwise Media Group Ltd.
# SPDX-License-Identifier: MIT
#
# Regenerate everything derived from api/v1alpha1: deepcopy methods and the
# CRD manifests. CRDs land in deploy/kustomize/base/crds/ (kustomize consumes
# them raw) and are mirrored into charts/patchy/templates/crds/ wrapped in the
# crds.install gate plus the keep policy (uninstall must never delete the
# all-time FindingRollup data). Living in templates/ rather than the chart's
# crds/ dir is deliberate: helm upgrades templates, while crds/ is
# install-only. The patchy-config values schema is generated here too, its
# Integration/Forge spec schemas lifted from the CRDs. CI runs this and fails
# on drift (git diff --exit-code).
set -eu

controller-gen object:headerFile=hack/boilerplate.go.txt paths=./api/...
controller-gen crd paths=./api/... output:crd:dir=deploy/kustomize/base/crds

# controller-gen writes no license header; the repo's addlicense gate wants
# one on every yaml file.
header="# Copyright 2026 Bitwise Media Group Ltd.
# SPDX-License-Identifier: MIT"
for crd in deploy/kustomize/base/crds/*.yaml; do
    if ! head -1 "$crd" | grep -q '^# Copyright'; then
        printf '%s\n%s' "$header" "$(cat "$crd")" >"$crd.tmp"
        mv "$crd.tmp" "$crd"
    fi
done

helm_dir=charts/patchy/templates/crds
rm -rf "$helm_dir"
mkdir -p "$helm_dir"
for crd in deploy/kustomize/base/crds/*.yaml; do
    out="$helm_dir/$(basename "$crd")"
    {
        printf '%s\n' "$header"
        printf '{{- if .Values.crds.install }}\n'
        # helm must not deep-template the CRD; only the keep annotation is
        # conditional. Injected after the generated annotations block. tail
        # skips the 2-line license header the loop above stamped on the
        # source copy.
        tail -n +3 "$crd" | awk '
            /^  annotations:$/ && !done {
                print
                print "    {{- if .Values.crds.keep }}"
                print "    helm.sh/resource-policy: keep"
                print "    {{- end }}"
                done = 1
                next
            }
            { print }
        '
        printf '{{- end }}\n'
    } >"$out"
done

# The patchy-config chart's values schema embeds the Integration/Forge spec
# schemas lifted straight from the CRDs generated above, so a mistyped entry
# fails `helm install` client-side instead of mid-apply at the API server.
# The CEL x-kubernetes-* keywords are stripped (they are not JSON Schema),
# and every object carrying properties is closed with
# additionalProperties: false. The API server would silently prune unknown
# fields; the chart rejects them. The whole file is generated: edit the
# skeleton here, never the file.
schema=charts/patchy-config/values.schema.json
cat >"$schema" <<'EOF'
{
    "$schema": "http://json-schema.org/draft-07/schema#",
    "title": "patchy-config Helm chart values",
    "type": "object",
    "additionalProperties": false,
    "definitions": {
        "integration": {
            "type": "object",
            "additionalProperties": false,
            "required": ["name", "spec"],
            "properties": {
                "name": { "type": "string", "minLength": 1 },
                "spec": { "$ref": "#/definitions/integrationSpec" }
            }
        },
        "forge": {
            "type": "object",
            "additionalProperties": false,
            "required": ["name", "spec"],
            "properties": {
                "name": { "type": "string", "minLength": 1 },
                "spec": { "$ref": "#/definitions/forgeSpec" }
            }
        },
        "integrationSpec": {},
        "forgeSpec": {}
    },
    "properties": {
        "global": { "type": "object" },
        "nameOverride": { "type": "string" },
        "fullnameOverride": { "type": "string" },
        "commonLabels": { "type": "object" },
        "commonAnnotations": { "type": "object" },
        "integrations": {
            "type": "array",
            "description": "Integration custom resources rendered into the release namespace",
            "items": { "$ref": "#/definitions/integration" }
        },
        "forges": {
            "type": "array",
            "description": "Forge custom resources rendered into the release namespace",
            "items": { "$ref": "#/definitions/forge" }
        }
    }
}
EOF
# The transforms run against the schema file itself, after injection: yq
# update-assignments do not propagate into a load()ed document, so a
# `load(...) | (.. ) = ...` pipeline would silently leave the loaded copy
# untouched.
INTEGRATION_CRD=deploy/kustomize/base/crds/patchy.bitwisemedia.uk_integrations.yaml \
FORGE_CRD=deploy/kustomize/base/crds/patchy.bitwisemedia.uk_forges.yaml \
yq -i -o=json '
    .definitions.integrationSpec = (load(strenv(INTEGRATION_CRD)) | .spec.versions[0].schema.openAPIV3Schema.properties.spec) |
    .definitions.forgeSpec = (load(strenv(FORGE_CRD)) | .spec.versions[0].schema.openAPIV3Schema.properties.spec) |
    del(.definitions[] | .. | .["x-kubernetes-validations"]?) |
    (.definitions.integrationSpec | .. | select(tag == "!!map" and has("properties"))."additionalProperties") = false |
    (.definitions.forgeSpec | .. | select(tag == "!!map" and has("properties"))."additionalProperties") = false
' "$schema"
# bun fmt is the toolchain's Prettier (the same one prose.sh runs); the
# repo's .prettierrc.yaml wins over the shared default when present.
pcfg=.prettierrc.yaml
[ -f "$pcfg" ] || pcfg=.mise/.prettierrc.yaml
bun fmt --no-script --config "$pcfg" --log-level warn --write "$schema"
