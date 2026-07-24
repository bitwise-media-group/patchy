// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package harness

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/model"
)

// EnabledSet turns a list of enabled harness ids into a lookup set.
func EnabledSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

// ResolveModel picks the harness that runs m given the enabled harness set,
// and the CLI model-id string that harness's --model flag expects. The model's
// preferred harness wins when it is enabled and supports the model; otherwise
// the first enabled supporting harness in registry order, so resolution is
// deterministic. As a last resort the fake harness runs any model when enabled
// (dev/e2e), reporting the model's bare id as the CLI id it will ignore.
func ResolveModel(m model.Model, enabled map[string]bool) (harnessID, cliModelID string, ok bool) {
	if enabled[m.Preferred] {
		if id, ok := m.CLIModelID(m.Preferred); ok {
			return m.Preferred, id, true
		}
	}
	for _, id := range model.KnownHarnessIDs {
		if id == model.HarnessFake || !enabled[id] {
			continue
		}
		if cli, ok := m.CLIModelID(id); ok {
			return id, cli, true
		}
	}
	if enabled[model.HarnessFake] {
		return model.HarnessFake, m.BareID(), true
	}
	return "", "", false
}

// ValidateAllowlist checks that every allowlist entry is a known canonical
// model id that resolves to an enabled harness. It returns a joined error
// naming every unknown id and every id no enabled harness can run, or nil when
// the allowlist is fully covered.
func ValidateAllowlist(models []model.Model, allowlist []string, enabled map[string]bool) error {
	var errs []error
	for _, id := range allowlist {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		m, found := model.ModelByID(models, id)
		if !found {
			errs = append(errs, fmt.Errorf("unknown model %q (not in the model registry)", id))
			continue
		}
		if _, _, ok := ResolveModel(m, enabled); !ok {
			errs = append(errs, fmt.Errorf("model %q needs one of harnesses %v enabled, but the enabled set is %v",
				id, m.SupportedHarnessIDs(), enabledIDs(enabled)))
		}
	}
	return errors.Join(errs...)
}

// enabledIDs renders the enabled set as a sorted slice for error messages.
func enabledIDs(enabled map[string]bool) []string {
	ids := make([]string, 0, len(enabled))
	for id, on := range enabled {
		if on {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}
