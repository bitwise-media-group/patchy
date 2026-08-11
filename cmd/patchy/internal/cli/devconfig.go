// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// cliConfigExtensions lists the accepted config-file extensions, in search
// order: the file is .patchy.<ext> in the working directory.
var cliConfigExtensions = []string{"yaml", "yml", "json"}

// loadCLIConfig layers the environment and an optional project config file
// under a command's flags: explicit flag > PATCHY_* environment >
// .patchy.<ext> > flag default. Each command group claims a config block —
// `dev:` for the dev commands, `mirror:` for the mirror commands — and the
// key replacer maps dots and dashes to underscores, so `dev.enhance-url`
// answers to PATCHY_DEV_ENHANCE_URL and `mirror.directory` to
// PATCHY_MIRROR_DIRECTORY. keys maps each flag name to its config key.
//
// Only the cluster-free command groups are viper-resolved; the rest of the
// CLI reads plain flags, because a kubeconfig-backed verb already has
// kubectl's own configuration story.
func loadCLIConfig(cmd *cobra.Command, keys map[string]string) (*viper.Viper, error) {
	v := viper.New()
	v.SetEnvPrefix("PATCHY")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()
	if err := readCLIConfigFile(v); err != nil {
		return nil, err
	}
	for flag, key := range keys {
		if err := v.BindPFlag(key, cmd.Flags().Lookup(flag)); err != nil {
			return nil, fmt.Errorf("bind --%s: %w", flag, err)
		}
	}
	return v, nil
}

// readCLIConfigFile finds and loads the single .patchy.<ext> in the working
// directory. None is fine — the file is optional; several are ambiguous and
// rejected rather than silently prioritized.
func readCLIConfigFile(v *viper.Viper) error {
	var found []string
	for _, ext := range cliConfigExtensions {
		path := ".patchy." + ext
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			found = append(found, path)
		}
	}
	switch len(found) {
	case 0:
		return nil
	case 1:
	default:
		return errUsage(fmt.Errorf("ambiguous config: found %s; keep exactly one", strings.Join(found, ", ")))
	}
	v.SetConfigFile(found[0])
	if err := v.ReadInConfig(); err != nil {
		return fmt.Errorf("read config %s: %w", found[0], err)
	}
	return nil
}

// noteConfigFile narrates which config file was loaded, if any, so a value
// arriving from disk is never a mystery.
func noteConfigFile(opts *Options, v *viper.Viper) {
	if path := v.ConfigFileUsed(); path != "" {
		notef(opts.ErrOut, "patchy: using %s\n", filepath.Base(path))
	}
}

// resolveDevSecret turns the resolved secret/secret-file pair into key
// bytes. An explicitly-set flag wins over the other source arriving from the
// environment or config file; two values with no explicit flag to break the
// tie are ambiguous. Both empty returns nil — the caller decides whether
// that means "generate one" (the harness) or "required" (the one-shots).
func resolveDevSecret(cmd *cobra.Command, secret, secretFile string) ([]byte, string, error) {
	if secret != "" && secretFile != "" {
		switch {
		case cmd.Flags().Changed("secret"):
			secretFile = ""
		case cmd.Flags().Changed("secret-file"):
			secret = ""
		default:
			return nil, "", errUsage(errors.New(
				"both a secret and a secret file are configured; pass --secret or --secret-file to pick one"))
		}
	}
	if secretFile != "" {
		raw, err := os.ReadFile(secretFile)
		if err != nil {
			return nil, "", fmt.Errorf("read secret file: %w", err)
		}
		// Editors append a newline; a trailing one is never part of the key.
		key := strings.TrimRight(string(raw), "\r\n")
		if key == "" {
			return nil, "", fmt.Errorf("secret file %s is empty", secretFile)
		}
		return []byte(key), "from " + secretFile, nil
	}
	if secret != "" {
		return []byte(secret), "from --secret", nil
	}
	return nil, "", nil
}
