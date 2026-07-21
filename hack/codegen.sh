#!/usr/bin/env sh
# Copyright 2026 Bitwise Media Group Ltd.
# SPDX-License-Identifier: MIT
#
# Regenerate everything derived from api/v1alpha1: deepcopy methods and the
# CRD manifests. CRDs land in deploy/kustomize/base/crds/ (kustomize consumes
# them raw) and are mirrored into helm/chart/templates/crds/ wrapped in the
# crds.install gate plus the keep policy (uninstall must never delete the
# all-time FindingRollup data). Living in templates/ rather than the chart's
# crds/ dir is deliberate: helm upgrades templates, while crds/ is
# install-only. CI runs this and fails on drift (git diff --exit-code).
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

helm_dir=helm/chart/templates/crds
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
