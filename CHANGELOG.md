# Changelog

## [0.4.0](https://github.com/bitwise-media-group/patchy/compare/v0.3.3...v0.4.0) (2026-07-22)


### ⚠ BREAKING CHANGES

* GitHub issues are no longer the state store. The pipeline is driven by the patchy.bitwisemedia.uk/v1alpha1 custom resources; issues are a one-way projection. webhook-controller is removed, and deployments must install the CRDs and create Integration/Forge resources.

### Features

* **agent:** drop node for the native claude binary ([081d1ab](https://github.com/bitwise-media-group/patchy/commit/081d1abdd20d1abf78d0635ff1404e272610c5e1))
* **api:** add patchy.bitwisemedia.uk/v1alpha1 API and CRD tooling ([e57c20b](https://github.com/bitwise-media-group/patchy/commit/e57c20b98140e326e9c18fd04ca6c3b09e894d7e))
* **context:** add the CRD-native enhancement reconciler ([68e16d4](https://github.com/bitwise-media-group/patchy/commit/68e16d4aa8f3b805ddd6fbca39e0119222fcc4de))
* cut the pipeline over to the CRD state machine ([b55d8a7](https://github.com/bitwise-media-group/patchy/commit/b55d8a723f60032b97ded75c4fd4c472795f018b))
* **deploy:** rebuild kustomize and helm for the CRD stack ([67e3e12](https://github.com/bitwise-media-group/patchy/commit/67e3e1243089c2531e47de9c759d61f05603651a))
* **deploy:** ship the status-server in kustomize and helm ([d6ecbe3](https://github.com/bitwise-media-group/patchy/commit/d6ecbe3504e68f2b628d19f36211d83618484385))
* **integration:** add the integration-controller engine ([ff79f37](https://github.com/bitwise-media-group/patchy/commit/ff79f375d366dc5f8d363d4fd5e785bf6e84b9dd))
* **investigation:** split the agent stages and add investigation-controller ([50aedd1](https://github.com/bitwise-media-group/patchy/commit/50aedd1a6b2ba203a4ef5708acd06024a7a56353))
* **remediation:** add the CRD-native remediation engine ([1c280a1](https://github.com/bitwise-media-group/patchy/commit/1c280a1718354622eeb62e1650ae3c0ba8a38e53))
* **rollup:** add all-time statistics rollups, finding TTL, and metrics ([76bb964](https://github.com/bitwise-media-group/patchy/commit/76bb9643a563f09f06df78a2fce5c7512d5865fc))
* **source:** add forge resolution and repository artifact engine ([a66e81b](https://github.com/bitwise-media-group/patchy/commit/a66e81bd20cfb82690a2d2994c03c56ee2afaa32))
* **web:** add the status-server backend and binary ([1933a38](https://github.com/bitwise-media-group/patchy/commit/1933a38442f4afbf6d65ec05ab38a9fca72e4802))
* **web:** embed the status page SPA and wire the withui build ([a2a2809](https://github.com/bitwise-media-group/patchy/commit/a2a280994717274579f75b01510e26da28f0791c))


### Bug Fixes

* **deps:** bump golang.org/x/text to v0.39.0 ([913cd05](https://github.com/bitwise-media-group/patchy/commit/913cd053105a251f752388334b40b85ff64ebd78))
* **web:** harden auth cookie attributes flagged by CodeQL ([fb1bc98](https://github.com/bitwise-media-group/patchy/commit/fb1bc98e8f117e6a939489f0f89b2be7f863d8c9))

## [0.3.3](https://github.com/bitwise-media-group/patchy/compare/v0.3.2...v0.3.3) (2026-07-19)


### Bug Fixes

* **jobs:** wait for the agent container before reading its logs ([3a5d213](https://github.com/bitwise-media-group/patchy/commit/3a5d2135f5b4a4809166982851ed113e7c69602d))

## [0.3.2](https://github.com/bitwise-media-group/patchy/compare/v0.3.1...v0.3.2) (2026-07-17)


### Bug Fixes

* **deploy:** grant the remediation-controller update on per-Job Secrets ([8d44e02](https://github.com/bitwise-media-group/patchy/commit/8d44e0265b3ab059e3f2b3d9df4c762cf0859f97))
* **release:** sign images and chart with cosign's legacy signature format ([57addef](https://github.com/bitwise-media-group/patchy/commit/57addefea5552553f44c31c0b5292969bb2a5d57))

## [0.3.1](https://github.com/bitwise-media-group/patchy/compare/v0.3.0...v0.3.1) (2026-07-17)


### Features

* **build:** add dev-colima task for one-command local deploys ([7602d35](https://github.com/bitwise-media-group/patchy/commit/7602d3574d25c68f1ea58a259692096f99c92682))
* **deploy:** front the dev webhook with traefik ingress ([816ab22](https://github.com/bitwise-media-group/patchy/commit/816ab22441e5f3a050b31fe43b7e4f397b66baa9)), closes [#16](https://github.com/bitwise-media-group/patchy/issues/16)


### Bug Fixes

* **deploy:** clear the kubescape gates for helm and kustomize ([f724c22](https://github.com/bitwise-media-group/patchy/commit/f724c2251486bf007740d7ad959ae938826e49ba))

## [0.3.0](https://github.com/bitwise-media-group/patchy/compare/v0.2.0...v0.3.0) (2026-07-14)


### ⚠ BREAKING CHANGES

* envelope events are v2 (v1 is rejected; controller and agent-runner must be released in lockstep, which goreleaser and the helm chart already guarantee). PATCHY_BUNDLE_MAX_BYTES is renamed to PATCHY_CHANGESET_MAX_BYTES and PATCHY_DEFAULT_BRANCH is removed; the outcome bundle_too_large is renamed to changeset_too_large.
* classification reports recommending 'intervention' are now rejected; agents must write 'recommendation: manual'.

### Features

* add webhook-controller, the single routed webhook entry point ([b80f48b](https://github.com/bitwise-media-group/patchy/commit/b80f48b428b47c9b59b14409ed60a2e86cb5bc61))
* push remediation branches through the GitHub API ([14ad25a](https://github.com/bitwise-media-group/patchy/commit/14ad25acf697f797ccda3c7f04943b724feaeac1))
* support a claude setup-token OAuth token as the model credential ([8693d33](https://github.com/bitwise-media-group/patchy/commit/8693d331c01c806fdba8a3292809df71e181454f))


### Code Refactoring

* rename the intervention recommendation to manual ([df053e7](https://github.com/bitwise-media-group/patchy/commit/df053e78e62f304c9cecc0568ec59be0168703f4))

## [0.2.0](https://github.com/bitwise-media-group/patchy/compare/v0.1.0...v0.2.0) (2026-07-14)


### ⚠ BREAKING CHANGES

* **helm:** every values key moved; see helm/chart/values.yaml. The chart has never shipped in a release, so no migration is provided.
* **cli:** --verbose / PATCHY_VERBOSE is gone; use --log-level=debug / PATCHY_LOG_LEVEL=debug instead. The default level drops from info to warn.

### Features

* **cli:** replace --verbose with a four-level --log-level flag ([9f496b2](https://github.com/bitwise-media-group/patchy/commit/9f496b2ce7ef9b7907709df567500d81c39f7dfc))
* **helm:** restructure the chart around per-controller value blocks ([0e26d3c](https://github.com/bitwise-media-group/patchy/commit/0e26d3cbd7f646ecc67c7ee274de2835212bcb41))

## 0.1.0 (2026-07-13)


### Features

* add core libraries for the finding pipeline ([e2b025d](https://github.com/bitwise-media-group/patchy/commit/e2b025d176aca1b8ba64c3e6067df694c8570bd1))
* add deployment manifests and the end-to-end suite ([23b958f](https://github.com/bitwise-media-group/patchy/commit/23b958f32f54beb79b462627c23249935fe06c02))
* **agent-runner:** add the two-stage coding-agent runtime ([d88d47b](https://github.com/bitwise-media-group/patchy/commit/d88d47b59f8d5210c16dcbbd0db36e13b28c9b08))
* **context-controller:** enhance finding issues with ownership context ([fac3dda](https://github.com/bitwise-media-group/patchy/commit/fac3dda6cf26e5866f7cbf282d22fed1d6efab37))
* **deploy:** add istio egress component for the agent sandbox ([cf2bc16](https://github.com/bitwise-media-group/patchy/commit/cf2bc165882bd4695586e2d33357dfcdd63a9037))
* **helm:** package the stack as an OCI-published helm chart ([18454e7](https://github.com/bitwise-media-group/patchy/commit/18454e79665935bb26a3bfd66a9dd28a0aa0e5de))
* **release:** publish multi-arch container images with goreleaser dockers_v2 ([3f22108](https://github.com/bitwise-media-group/patchy/commit/3f221081d3834fbab4b147d03ce0b9c8f9963216))
* **release:** sign container images and helm chart with cosign ([d0c768f](https://github.com/bitwise-media-group/patchy/commit/d0c768f7cc210b75f7e1615d839ac083b458f463))
* **remediation-controller:** run agent jobs and apply their github effects ([dbbc7a6](https://github.com/bitwise-media-group/patchy/commit/dbbc7a67767aeeca4dcfa9f5d8392f0a1096f1cb))
* **source-controller:** accumulate GHAS alerts into finding issues ([b697717](https://github.com/bitwise-media-group/patchy/commit/b6977176eb6dd2ddb4bc10170aa77dd4234ce6e8))
