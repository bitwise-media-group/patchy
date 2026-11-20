<!--
Copyright 2026 Bitwise Media Group Ltd.
SPDX-License-Identifier: MIT
-->

# patchy-config Helm chart

This chart switches on the pipeline for a [patchy](../patchy) release. It renders the `Integration` and `Forge` custom
resources from the `integrations` / `forges` values arrays into the release namespace. The controllers do nothing until
at least one of each exists. An Integration says where findings come from and where the tracking issues go. A Forge says
how repositories are cloned and pushed.

## Why a separate chart

Helm validates every manifest in a release against the API server **before** applying anything. A single chart carrying
both the `patchy.bitwisemedia.uk` CRDs and custom resources of those CRDs therefore fails its first install: the CRs are
validated before the CRDs exist. With the CRs in their own chart, you install the two in order:

```sh
# 1. the stack: CRDs, controllers, agent sandbox
helm install patchy oci://ghcr.io/bitwise-media-group/patchy/charts/patchy \
    --version <X.Y.Z> --namespace patchy --create-namespace

# 2. the switch-on: Integration + Forge CRs, SAME namespace
helm install patchy-config oci://ghcr.io/bitwise-media-group/patchy/charts/patchy-config \
    --version <X.Y.Z> --namespace patchy -f values.yaml
```

Both charts version 1:1 with the app release. Install this chart into the **same namespace** as the patchy release,
because the controllers only reconcile CRs they can see.

## Values

```yaml
integrations:
  - name: github
    spec:
      provider: github
      secretRef:
        name: patchy-github
      interval: 10m
      github:
        issues:
          enabled: true
          approveComment: /approve
        codeScanningAlerts:
          enabled: true

forges:
  - name: github
    spec:
      provider: github
      secretRef:
        name: patchy-github
      interval: 10m
```

Each entry's `spec` is rendered verbatim and validated twice:

- Client-side by `values.schema.json`. It embeds the CRD spec schemas, which `mise run codegen` regenerates whenever the
  API types change. A misspelled or wrongly typed field fails `helm install` before anything is applied.
- Server-side by the CRDs themselves, including the CEL rules the values schema cannot carry.

See [`deploy/kustomize/base/crs.example.yaml`](../../deploy/kustomize/base/crs.example.yaml) for a walkthrough of every
field (GHES base URLs, org allowlists, repository regexes). `commonLabels` / `commonAnnotations` add extra labels and
annotations to every rendered CR.

You create the referenced Secrets separately, and neither chart manages them. See
[`deploy/kustomize/base/secrets.example.yaml`](../../deploy/kustomize/base/secrets.example.yaml). The keys are `appID` +
`privateKey` (or `token`), plus `webhookSecret` for Integrations. One GitHub Secret can serve both CRs, or you can split
read and write identities across two GitHub Apps.

This chart is optional. To apply the CRs yourself, skip it and `kubectl apply` the same objects.

## Publishing

When a release is published, [`.github/workflows/helm.yaml`](../../.github/workflows/helm.yaml) packages
`charts/patchy-config` with the patchy chart and pushes it to `oci://ghcr.io/bitwise-media-group/patchy/charts`.
release-please stamps `version`/`appVersion` in `Chart.yaml` as part of the release PR. Lint locally with
`mise run helm-lint`.
