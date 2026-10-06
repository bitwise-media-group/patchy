# patchy — triage and remediation pipeline for security findings, driven by
# GitHub issues as the state machine.
#
# Everything lives in mise tasks: the go archetype (test/lint/release
# machinery) and the common tasks (license, prose, workflow, container, and
# shell lint) come from the shared toolchain submodule at .mise/, selected in
# the root mise.toml; tasks.toml carries the repo-local tasks (multi-binary
# build, e2e, codegen, ...). This Makefile is only the thin forwarding shim —
# `make <task>` == `mise run <task>`.
include .mise/archetypes/go/include.mk
