# Changelog

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
