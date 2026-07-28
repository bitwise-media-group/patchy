// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package enhancers holds the built-in pkg/enhance implementations:
// googlecloud (repository resolution from a Google Cloud resource's
// ownership labels, with dynamicgooglecloud reading its configuration off
// the google-cloud Integration per enhancement) and staticfile (a
// YAML-backed repo→ownership map that doubles as the fake CMDB for
// development and e2e). The real CMDB integration implements the same
// pkg/enhance.Enhancer seam later.
package enhancers
