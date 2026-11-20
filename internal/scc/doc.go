// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package scc is the Google Cloud Security Command Center source: it turns a
// Pub/Sub push delivery into patchy Findings.
//
// SCC has no webhook. Its only egress is a NotificationConfig publishing to a
// Pub/Sub topic, so deliveries arrive as a push envelope whose message.data is
// a base64-encoded NotificationMessage. Unlike GHAS, the notification carries
// everything the finding needs, so this handler holds no API client and makes
// no network call.
//
// SCC findings are about cloud resources, not repository code, so they carry a
// source.CloudResource and no Repo. A pkg/enhance enhancer may later assign a
// repository by reading ownership labels off the resource.
package scc
