// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package scc is the Google Cloud Security Command Center source: it turns a
// Pub/Sub push delivery into patchy Findings.
//
// SCC has no webhook. Its only egress is a NotificationConfig publishing to a
// Pub/Sub topic, so deliveries arrive as a push envelope whose message.data is
// a base64-encoded NotificationMessage. Unlike GHAS the notification is
// self-contained — everything the finding needs is in the payload — so this
// handler holds no API client and makes no network call.
//
// SCC findings are about cloud resources, not repository code, so they carry a
// source.CloudResource and no Repo. Whether such a finding ever gets a
// repository is a later question, answered by a pkg/enhance enhancer reading
// ownership labels off the resource.
package scc
