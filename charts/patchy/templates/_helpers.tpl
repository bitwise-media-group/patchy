{{/*
Copyright 2026 Bitwise Media Group Ltd.
SPDX-License-Identifier: MIT
*/}}

{{- define "patchy.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{- define "patchy.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end }}

{{/*
Per-controller ConfigMap name. Context: dict "root" $ "name" <component>.
*/}}
{{- define "patchy.configMapName" -}}
{{- printf "%s-%s-config" (include "patchy.fullname" .root) .name -}}
{{- end }}

{{/*
Per-controller ServiceAccount name: <controller>.serviceAccount.name, or
<fullname>-<component>. Context: dict "root" $ "name" <component> "vals"
<controller values>.
*/}}
{{- define "patchy.serviceAccountName" -}}
{{- .vals.serviceAccount.name | default (printf "%s-%s" (include "patchy.fullname" .root) .name) -}}
{{- end }}

{{/*
Selector labels for one component. Context: dict "root" $ "name" <component>.
app.kubernetes.io/name matches deploy/kustomize (source-controller, ...).
instance tells releases apart.
*/}}
{{- define "patchy.selectorLabels" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/part-of: patchy
{{- end }}

{{/*
Full label set. Context: dict "root" $ "name" <component> ["component" <role>].
*/}}
{{- define "patchy.labels" -}}
{{- /*
The bare `app` duplicates app.kubernetes.io/name for tooling that predates
the recommended-label set (kubescape C-0076 counts it, some dashboards group
by it). It is not in selectorLabels, because selectors cannot change on
upgrade.
*/ -}}
app: {{ .name }}
helm.sh/chart: {{ printf "%s-%s" .root.Chart.Name .root.Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
app.kubernetes.io/version: {{ .root.Chart.AppVersion | quote }}
{{ include "patchy.selectorLabels" . }}
{{- with .component }}
app.kubernetes.io/component: {{ . }}
{{- end }}
{{- with .root.Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end }}

{{/*
Object annotations: commonAnnotations plus optional per-object extras, which
win key by key. Context: dict "root" $ ["extra" <map>]. The output is empty
when both maps are empty, so wrap the annotations: key in `with`.
*/}}
{{- define "patchy.annotations" -}}
{{- $a := merge (dict) (.extra | default dict) (.root.Values.commonAnnotations | default dict) -}}
{{- if $a -}}
{{- toYaml $a -}}
{{- end -}}
{{- end }}

{{/*
Image reference for one binary. Context: dict "root" $ "binary" <name>
["image" <per-component override map>]. The repository (registry included)
defaults to <image.repository>/<binary>. A digest pins the image and takes
precedence over the tag. The tag defaults to v<appVersion>, which is how
goreleaser tags the images.
*/}}
{{- define "patchy.image" -}}
{{- $img := .image | default dict -}}
{{- $g := .root.Values.image -}}
{{- $repository := $img.repository | default (printf "%s/%s" $g.repository .binary) -}}
{{- if $img.digest -}}
{{- printf "%s@%s" $repository $img.digest -}}
{{- else -}}
{{- printf "%s:%s" $repository ($img.tag | default $g.tag | default (printf "v%s" .root.Chart.AppVersion)) -}}
{{- end -}}
{{- end }}

{{/*
The pod labels internal/jobs sets on every agent Job pod. The controller
fixes them and this chart cannot change them. The sandbox NetworkPolicies
select on them.
*/}}
{{- define "patchy.agentPodSelector" -}}
app.kubernetes.io/name: patchy-agent
app.kubernetes.io/managed-by: patchy
{{- end }}

{{/*
The harnesses whose runners are brokered: every one that calls a model API.
The list is space-separated. Use it with `has $id (splitList " " (include
"patchy.brokeredHarnesses" .))`.
*/}}
{{- define "patchy.brokeredHarnesses" -}}
claude codex
{{- end }}

{{/*
Whether a harness's runner is enabled anywhere (findings or evaluations).
Context: dict "root" $ "id" <harness>. Returns a non-empty string for yes.
*/}}
{{- define "patchy.harnessEnabled" -}}
{{- $r := index .root.Values.agent.runners .id | default dict -}}
{{- $e := index .root.Values.evaluationController.runners .id | default dict -}}
{{- if or $r.enabled (and .root.Values.evaluationController.enabled $e.enabled) -}}
yes
{{- end -}}
{{- end }}

{{/*
Whether the egress credential broker deploys. It deploys when, and only
when, a brokered (claude or codex) runner is enabled anywhere (findings or
evaluations). There is no separate enabled setting. Both run proxy-only, so
claude or codex ⇒ broker, and a fake-only dev/e2e values file renders none
of it. Returns a non-empty string for yes.
*/}}
{{- define "patchy.brokerEnabled" -}}
{{- range $id := splitList " " (include "patchy.brokeredHarnesses" .) -}}
{{- include "patchy.harnessEnabled" (dict "root" $ "id" $id) -}}
{{- end -}}
{{- end }}

{{/*
The egress broker's in-cluster base URL. PATCHY_BROKER_URL is set to it, and
the agent pods' gateway env points at it.
*/}}
{{- define "patchy.brokerURL" -}}
http://{{ include "patchy.fullname" . }}-egress-broker.{{ .Release.Namespace }}.svc.cluster.local:8080
{{- end }}

{{/*
Write a brokered harness's configuration into a controller's ConfigMap data
dict: the broker URL and the PATCHY_<HARNESS>_PROVIDER*, _MODEL_MAP and
_PROVIDER_ENV keys the runnercfg flags bind (region, regionPrefix and
projectID are claude-only). Context: dict "root" $ "data" <dict> "id"
<harness>. The PATCHY_<HARNESS>_SECRET* keys are NOT written, because the
model credentials live in the broker and never in the agent namespace.
*/}}
{{- define "patchy.providerData" -}}
{{- $runner := index .root.Values.agent.runners .id | default dict -}}
{{- $p := $runner.provider | default dict -}}
{{- $up := upper .id -}}
{{- $_ := set .data "PATCHY_BROKER_URL" (include "patchy.brokerURL" .root) -}}
{{- $_ := set .data (printf "PATCHY_%s_PROVIDER" $up) ($p.name | default (ternary "openai" "anthropic" (eq .id "codex"))) -}}
{{- with $p.modelMap -}}
{{- $_ := set $.data (printf "PATCHY_%s_MODEL_MAP" $up) (include "patchy.kvList" .) -}}
{{- end -}}
{{- with $p.env -}}
{{- $_ := set $.data (printf "PATCHY_%s_PROVIDER_ENV" $up) (include "patchy.kvList" .) -}}
{{- end -}}
{{- if eq .id "claude" -}}
{{- with $p.region -}}
{{- $_ := set $.data "PATCHY_CLAUDE_PROVIDER_REGION" . -}}
{{- end -}}
{{- with $p.regionPrefix -}}
{{- $_ := set $.data "PATCHY_CLAUDE_PROVIDER_REGION_PREFIX" . -}}
{{- end -}}
{{- with $p.projectID -}}
{{- $_ := set $.data "PATCHY_CLAUDE_PROVIDER_PROJECT_ID" . -}}
{{- end -}}
{{- end -}}
{{- end }}

{{/*
Render a map as the comma-joined key=value wire form the runnercfg flags
parse (--<harness>-model-map, --<harness>-provider-env). Keys sort
deterministically.
*/}}
{{- define "patchy.kvList" -}}
{{- $pairs := list -}}
{{- range $k, $v := . -}}
{{- $pairs = append $pairs (printf "%s=%s" $k $v) -}}
{{- end -}}
{{- join "," $pairs -}}
{{- end }}

{{/*
The resolved hostname-enforcement mode for the agent sandbox: one of
`none`, `cilium`, `gke` or `istio`. Every egress template uses this, so only
one place decides which kind of CNI policy a cluster gets.

Precedence:

  1. agent.networkPolicy.mode, when it is not "auto".
  2. The legacy booleans (agent.networkPolicy.{cilium,istio}.enabled), so a
     values file written before `mode` existed keeps its behaviour.
  3. "auto": capability detection against the cluster's discovery document.

Helm fills .Capabilities.APIVersions from the API server whenever it renders
against a cluster: `helm install/upgrade`, and every helm-controller
reconcile. Off-cluster `helm template` sees only Helm's built-in list, so auto
resolves to "none" in CI. That is intended: a render that cannot see the
cluster must not claim an enforcement it cannot verify. Pin `mode` when you
need a deterministic render.

Two things auto will never do:

  * Select `istio`. Installed CRDs say nothing about whether the namespace
    has the injection label or whether istiod runs with native sidecars.
    Without native sidecars the agent Job never completes. So Istio stays
    opt-in.
  * Select `cilium` on GKE. GKE Dataplane V2 IS Cilium, and it does publish
    cilium.io CRDs. But it has not honoured the CiliumNetworkPolicy CRD since
    1.21.5-gke.1300 and rejects every L7 rule. Those rules are the `toFQDNs`
    and `rules.dns` blocks this chart's CNP is made of. A CNP rendered there
    silently enforces nothing. That is worse than rendering none, because it
    looks like protection in `kubectl get`. GKE's own FQDNNetworkPolicy does
    the same job, so on GKE auto picks `gke` when that CRD is present and
    falls back to `none` when it is not.
*/}}
{{- define "patchy.egressMode" -}}
{{- $np := .Values.agent.networkPolicy -}}
{{- $mode := $np.mode | default "auto" -}}
{{- if not (has $mode (list "auto" "none" "cilium" "gke" "istio")) -}}
{{- fail (printf "agent.networkPolicy.mode: %q is not one of auto, none, cilium, gke, istio" $mode) -}}
{{- end -}}
{{- if ne $mode "auto" -}}
{{- $mode -}}
{{- else if $np.cilium.enabled -}}
cilium
{{- else if $np.istio.enabled -}}
istio
{{- else if .Capabilities.APIVersions.Has "networking.gke.io/v1alpha1/FQDNNetworkPolicy" -}}
gke
{{- else if and (not (contains "gke" (toString .Capabilities.KubeVersion.GitVersion))) (.Capabilities.APIVersions.Has "cilium.io/v2/CiliumNetworkPolicy") -}}
cilium
{{- else -}}
none
{{- end -}}
{{- end }}

{{/*
Whether the base NetworkPolicy keeps its "TCP 443 to anywhere outside the
cluster" egress rule. Returns a non-empty string for yes.

This rule decides whether a hostname allowlist has any effect. Network
policies are ADDITIVE. Both Cilium and GKE Dataplane V2 allow a packet that
matches ANY policy selecting the pod, and neither lets one policy subtract
from another. So a CiliumNetworkPolicy or an FQDNNetworkPolicy naming
api.anthropic.com changes nothing at all if a plain NetworkPolicy beside it
already allows 443 to 0.0.0.0/0. The combined result is still "443 to
anywhere".

So `auto` drops the broad rule when a hostname mode does the work (cilium,
gke). It keeps the rule when nothing else would replace it (none, istio).
The Istio sidecar enforces on SNI in a separate layer, and the L3 rule is
still wanted beneath it. `always` keeps the rule in every case. Use it while
you test a new mode, so egress fails open if the mode turns out not to
enforce. `never` drops the rule in every case.
*/}}
{{- define "patchy.broadEgress" -}}
{{- $broad := .Values.agent.networkPolicy.broadEgress | default "auto" -}}
{{- if not (has $broad (list "auto" "always" "never")) -}}
{{- fail (printf "agent.networkPolicy.broadEgress: %q is not one of auto, always, never" $broad) -}}
{{- end -}}
{{- if eq $broad "always" -}}
yes
{{- else if eq $broad "never" -}}
{{- else if has (include "patchy.egressMode" .) (list "none" "istio") -}}
yes
{{- end -}}
{{- end }}

{{/*
patchy.archiveData sets the run-archive keys on a ConfigMap's data dict. It
renders nothing unless archive.enabled. Called with (dict "root" $ "data"
$data "role" "controller"|"status-server"): the controllers take the
failure policy, the status server the presign lifetime.
*/}}
{{- define "patchy.archiveData" -}}
{{- $a := .root.Values.archive | default dict -}}
{{- if $a.enabled -}}
{{- if not (has $a.provider (list "s3" "gcs")) -}}
{{- fail "archive.provider must be s3 or gcs when archive.enabled" -}}
{{- end -}}
{{- if not $a.bucket -}}
{{- fail "archive.bucket is required when archive.enabled" -}}
{{- end -}}
{{- $_ := set .data "PATCHY_ARCHIVE_PROVIDER" $a.provider -}}
{{- $_ := set .data "PATCHY_ARCHIVE_BUCKET" $a.bucket -}}
{{- with $a.prefix }}{{ $_ := set $.data "PATCHY_ARCHIVE_PREFIX" . }}{{ end -}}
{{- with $a.region }}{{ $_ := set $.data "PATCHY_ARCHIVE_REGION" . }}{{ end -}}
{{- with $a.endpoint }}{{ $_ := set $.data "PATCHY_ARCHIVE_ENDPOINT" . }}{{ end -}}
{{- if eq .role "controller" -}}
{{- with $a.kmsKey }}{{ $_ := set $.data "PATCHY_ARCHIVE_KMS_KEY" . }}{{ end -}}
{{- $_ := set .data "PATCHY_ARCHIVE_REQUIRED" ($a.required | default false | toString) -}}
{{- $_ := set .data "PATCHY_ARCHIVE_RETRY_WINDOW" ($a.retryWindow | default "45m") -}}
{{- else -}}
{{- $_ := set .data "PATCHY_ARCHIVE_PRESIGN_TTL" ($a.presignTTL | default "5m") -}}
{{- end -}}
{{- end -}}
{{- end }}

{{/*
patchy.archiveEgress renders the extra egress a pod needs to fetch archive
credentials from a node-local metadata endpoint over plain HTTP, which the
443/6443 rule does not cover: the EKS Pod Identity agent for s3, the GKE
metadata server for gcs. IRSA needs nothing extra (it exchanges the projected
token with STS over 443), and the bucket itself is reached over 443. Renders
nothing unless archive.enabled. Expects the indent of an egress list item.
*/}}
{{- define "patchy.archiveEgress" -}}
{{- $a := .Values.archive | default dict -}}
{{- if $a.enabled -}}
{{- if eq $a.provider "gcs" -}}
# Run archive credentials: the GKE metadata server (workload identity).
- to:
      - ipBlock:
            cidr: 169.254.169.254/32
  ports:
      - protocol: TCP
        port: 80
- to:
      - ipBlock:
            cidr: 169.254.169.252/32
  ports:
      - protocol: TCP
        port: 988
{{- else -}}
# Run archive credentials: the EKS Pod Identity agent.
- to:
      - ipBlock:
            cidr: 169.254.170.23/32
      - ipBlock:
            cidr: fd00:ec2::23/128
  ports:
      - protocol: TCP
        port: 80
{{- end }}
{{- end }}
{{- end }}
