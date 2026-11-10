// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/config"
)

// configHelp is the help paragraph every config-reading command group
// shares: where the layered patchy config comes from and how it merges.
const configHelp = "The patchy config merges from, least to most preferred: /etc/patchy/config.<ext>,\n" +
	"~/.config/patchy/config.<ext>, then in the working directory mise/patchy.<ext>,\n" +
	"patchy/config.<ext>, .mise/patchy.<ext>, .patchy/config.<ext> and patchy.<ext>\n" +
	"(<ext> is yaml, yml, json or toml). Maps merge, lists replace, and null deletes\n" +
	"an inherited key."

// configOptions locates the layered patchy config. Zero values mean the
// real working directory, $HOME and /etc/patchy; tests point it at temp
// directories so a developer's own config never leaks in.
var configOptions config.Options

// loadPatchyConfig loads the layered patchy config, mapping an ambiguous
// location to a usage error.
func loadPatchyConfig() (*config.Loaded, error) {
	loaded, err := config.Load(configOptions)
	if err != nil {
		if errors.Is(err, config.ErrAmbiguous) {
			return nil, errUsage(err)
		}
		return nil, err
	}
	return loaded, nil
}

// loadCLIConfig layers the environment and the merged patchy config files
// under a command's flags: explicit flag > PATCHY_* environment > merged
// config files > flag default. Each command group claims a config block —
// `dev:` for the dev commands, `mirror:` for the mirror commands — and the
// key replacer maps dots and dashes to underscores, so `dev.enhance-url`
// answers to PATCHY_DEV_ENHANCE_URL and `mirror.directory` to
// PATCHY_MIRROR_DIRECTORY. keys maps each flag name to its config key.
//
// Viper only ever sees the flag-backed keys' merged values; the layering
// itself is the config package's (viper lowercases keys, which would
// corrupt user-defined ones). Only the cluster-free command groups are
// viper-resolved; the rest of the CLI reads plain flags, because a
// kubeconfig-backed verb already has kubectl's own configuration story.
func loadCLIConfig(cmd *cobra.Command, keys map[string]string) (*viper.Viper, *config.Loaded, error) {
	loaded, err := loadPatchyConfig()
	if err != nil {
		return nil, nil, err
	}
	v := viper.New()
	v.SetEnvPrefix("PATCHY")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()
	if err := v.MergeConfigMap(loaded.Merged); err != nil {
		return nil, nil, fmt.Errorf("apply patchy config: %w", err)
	}
	for flag, key := range keys {
		if err := v.BindPFlag(key, cmd.Flags().Lookup(flag)); err != nil {
			return nil, nil, fmt.Errorf("bind --%s: %w", flag, err)
		}
	}
	return v, loaded, nil
}

// noteConfigSources narrates every loaded config layer in precedence order
// (least preferred first) plus any warnings, so a value arriving from disk
// is never a mystery.
func noteConfigSources(opts *Options, loaded *config.Loaded) {
	if loaded == nil {
		return
	}
	if len(loaded.Sources) > 0 {
		notef(opts.ErrOut, "patchy: using %s\n", strings.Join(loaded.Sources, ", "))
	}
	for _, w := range loaded.Warnings {
		notef(opts.ErrOut, "patchy: warning: %s\n", w)
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
