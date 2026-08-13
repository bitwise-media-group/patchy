// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package distro

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// DefaultRegistry prefixes registry-less image references found in
// distribution manifests (the deploying operator prefixes its configured
// registry the same way; the mirror preserves the canonical home).
const DefaultRegistry = "ghcr.io"

// Derive reads a flattened distribution-manifests filesystem (a tar
// stream) and derives one extra image per named component from the newest
// version tree present — the version the release-frozen artifact's
// operator actually deploys. artifactRef appears in the derived reasons so
// a reviewer can trace where each pin came from.
func Derive(archive io.Reader, components []string, registry, artifactRef string) ([]spec.ExtraImage, error) {
	if registry == "" {
		registry = DefaultRegistry
	}
	files, err := readManifestFiles(archive)
	if err != nil {
		return nil, err
	}
	product, version, err := newestVersion(files, components)
	if err != nil {
		return nil, err
	}
	var extras []spec.ExtraImage
	for _, component := range components {
		key := product + "/" + version + "/" + component + ".yaml"
		raw, ok := files[key]
		if !ok {
			return nil, fmt.Errorf("artifact has no %s manifests for %s %s", component, product, version)
		}
		image, err := deploymentImage(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		if !strings.Contains(strings.SplitN(image, "/", 2)[0], ".") {
			image = registry + "/" + image
		}
		extras = append(extras, spec.ExtraImage{
			Image:  image,
			Reason: fmt.Sprintf("%s %s distribution controller (derived from %s)", product, version, artifactRef),
		})
	}
	if len(extras) == 0 {
		return nil, errors.New("distributionManifests derived no images")
	}
	return extras, nil
}

// readManifestFiles collects the archive's yaml files keyed by slash path.
func readManifestFiles(archive io.Reader) (map[string][]byte, error) {
	files := map[string][]byte{}
	tr := tar.NewReader(archive)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read artifact: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || !strings.HasSuffix(hdr.Name, ".yaml") {
			continue
		}
		raw, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", hdr.Name, err)
		}
		files[path.Clean(strings.TrimPrefix(hdr.Name, "./"))] = raw
	}
	if len(files) == 0 {
		return nil, errors.New("artifact contains no manifest files")
	}
	return files, nil
}

// newestVersion finds the product root carrying the requested component
// manifests and its newest version directory (<product>/<version>/...).
// Distribution artifacts carry sibling trees the derive must ignore —
// flux-operator-manifests ships image-variant lists (flux-images/) and VEX
// data (flux-vex/) beside the controller manifests — so only trees holding
// at least one <product>/<version>/<component>.yaml qualify.
func newestVersion(files map[string][]byte, components []string) (product, version string, err error) {
	wanted := map[string]bool{}
	for _, c := range components {
		wanted[c+".yaml"] = true
	}
	products := map[string]bool{}
	versions := map[string]bool{}
	for p := range files {
		parts := strings.Split(p, "/")
		if len(parts) != 3 || !wanted[parts[2]] {
			continue
		}
		products[parts[0]] = true
		versions[parts[0]+"/"+parts[1]] = true
	}
	if len(products) == 0 {
		return "", "", fmt.Errorf("no product tree in the artifact contains the requested components (%s)",
			strings.Join(components, ", "))
	}
	if len(products) > 1 {
		names := make([]string, 0, len(products))
		for p := range products {
			names = append(names, p)
		}
		sort.Strings(names)
		return "", "", fmt.Errorf("expected one product tree with the requested components, found %d (%s)",
			len(names), strings.Join(names, ", "))
	}
	for p := range products {
		product = p
	}
	var vs []string
	for pv := range versions {
		vs = append(vs, strings.TrimPrefix(pv, product+"/"))
	}
	sort.Slice(vs, func(i, j int) bool { return versionLess(vs[i], vs[j]) })
	return product, vs[len(vs)-1], nil
}

// deploymentImage extracts the first Deployment's first container image
// from a multi-document manifest file.
func deploymentImage(raw []byte) (string, error) {
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	for {
		var doc struct {
			Kind string `yaml:"kind"`
			Spec struct {
				Template struct {
					Spec struct {
						Containers []struct {
							Image string `yaml:"image"`
						} `yaml:"containers"`
					} `yaml:"spec"`
				} `yaml:"template"`
			} `yaml:"spec"`
		}
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return "", errors.New("no controller image found")
		}
		if err != nil {
			return "", fmt.Errorf("parse manifests: %w", err)
		}
		if doc.Kind == "Deployment" && len(doc.Spec.Template.Spec.Containers) > 0 &&
			doc.Spec.Template.Spec.Containers[0].Image != "" {
			return doc.Spec.Template.Spec.Containers[0].Image, nil
		}
	}
}

// versionLess sorts version directory names (v-prefixed or not) the way
// GNU version sort does, well enough for release directories.
func versionLess(a, b string) bool {
	as := strings.Split(strings.TrimPrefix(a, "v"), ".")
	bs := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		na, ea := strconv.Atoi(as[i])
		nb, eb := strconv.Atoi(bs[i])
		if ea == nil && eb == nil {
			if na != nb {
				return na < nb
			}
			continue
		}
		if as[i] != bs[i] {
			return as[i] < bs[i]
		}
	}
	return len(as) < len(bs)
}
