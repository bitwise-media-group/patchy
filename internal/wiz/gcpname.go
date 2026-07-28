// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package wiz

import (
	"regexp"
	"strings"
)

// versionSegment matches an API version path segment: v1, v2beta, v1alpha2...
var versionSegment = regexp.MustCompile(`^v\d+((alpha|beta)\d*)?$`)

// NormalizeGCPName rewrites a Wiz providerId for a Google Cloud resource into
// the Cloud Asset Inventory name form ("//service.googleapis.com/<path>"),
// which is what the asset-inventory enhancer queries by. Wiz reports GCP
// resources by API self-link more often than by asset name, and the two
// differ only mechanically:
//
//	https://www.googleapis.com/compute/v1/projects/p/zones/z/instances/i
//	https://container.googleapis.com/v1/projects/p/locations/l/clusters/c
//	→ //compute.googleapis.com/projects/p/zones/z/instances/i
//	→ //container.googleapis.com/projects/p/locations/l/clusters/c
//
// Storage self-links use a bucket shorthand ("/storage/v1/b/<bucket>") whose
// asset name drops the collection segment. A name already in asset form
// passes through; anything unrecognized is returned unchanged — the value
// still works as a stable accumulation scope, it just may not resolve in
// Cloud Asset Inventory (the enhancer's display-name fallback covers that).
func NormalizeGCPName(providerID string) string {
	if strings.HasPrefix(providerID, "//") {
		return providerID
	}
	rest, ok := strings.CutPrefix(providerID, "https://")
	if !ok {
		return providerID
	}
	host, path, ok := strings.Cut(rest, "/")
	if !ok {
		return providerID
	}
	var service string
	switch {
	case host == "www.googleapis.com":
		// The service is the first path segment.
		service, path, ok = cutSegment(path)
		if !ok {
			return providerID
		}
	case strings.HasSuffix(host, ".googleapis.com"):
		service = strings.TrimSuffix(host, ".googleapis.com")
	default:
		return providerID
	}
	// Drop the version segment when present.
	if seg, remainder, found := cutSegment(path); found && versionSegment.MatchString(seg) {
		path = remainder
	}
	// Storage's "b/<bucket>" collection segment does not appear in the asset
	// name: //storage.googleapis.com/<bucket>.
	if service == "storage" {
		if bucket, found := strings.CutPrefix(path, "b/"); found {
			path = bucket
		}
	}
	if path == "" {
		return providerID
	}
	return "//" + service + ".googleapis.com/" + path
}

// cutSegment splits the first path segment off, requiring both halves to be
// non-empty.
func cutSegment(path string) (seg, rest string, ok bool) {
	seg, rest, found := strings.Cut(path, "/")
	if !found || seg == "" || rest == "" {
		return "", "", false
	}
	return seg, rest, true
}
