// Package acp implements the ACP v1 transport shared by ACP-native coding
// agents. Runtime-specific configuration is limited to executable discovery
// and command-line construction; session behavior lives in the common driver.
package acp

import (
	"os"
	"path/filepath"

	"github.com/Fullstop000/unio/driver"
)

// Runtime identifies an ACP-native agent command.
type Runtime string

const (
	Kimi     Runtime = "kimi"
	TraeX    Runtime = "traex"
	OpenCode Runtime = "opencode"
)

type runtimeConfig struct {
	name         string
	executable   string
	alternatives []string
	buildArgs    func(driver.AgentSpec) []string
	modelConfig  string
}

func configFor(runtime Runtime) runtimeConfig {
	home, _ := os.UserHomeDir()
	switch runtime {
	case Kimi:
		return runtimeConfig{
			name:       "kimi",
			executable: "kimi-cli",
			alternatives: compactPaths(
				"kimi", filepath.Join(home, ".local", "bin", "kimi-cli"), filepath.Join(home, ".local", "bin", "kimi"),
			),
			buildArgs: func(spec driver.AgentSpec) []string {
				args := []string{"--work-dir", spec.Cwd}
				if spec.Model != "" {
					args = append(args, "--model", spec.Model)
				}
				args = append(args, spec.ExtraArgs...)
				return append(args, "acp")
			},
		}
	case TraeX:
		return runtimeConfig{
			name:       "traex",
			executable: "traex",
			alternatives: compactPaths(
				"trae-cli", "coco", "traecli",
				filepath.Join(home, ".local", "bin", "traex"),
				filepath.Join(home, ".local", "bin", "trae-cli"),
				filepath.Join(home, ".local", "bin", "coco"),
			),
			buildArgs: func(spec driver.AgentSpec) []string {
				var args []string
				if spec.Model != "" {
					args = append(args, "--model", spec.Model)
				}
				args = append(args, spec.ExtraArgs...)
				return append(args, "acp", "serve")
			},
		}
	case OpenCode:
		return runtimeConfig{
			name:        "opencode",
			executable:  "opencode",
			modelConfig: "model",
			alternatives: compactPaths(
				filepath.Join(home, ".opencode", "bin", "opencode"),
			),
			buildArgs: func(spec driver.AgentSpec) []string {
				args := []string{"acp", "--cwd", spec.Cwd}
				return append(args, spec.ExtraArgs...)
			},
		}
	default:
		return runtimeConfig{name: string(runtime), executable: string(runtime), buildArgs: func(driver.AgentSpec) []string { return []string{"acp"} }}
	}
}

func (c runtimeConfig) applyDefaults(spec driver.AgentSpec) driver.AgentSpec {
	if spec.ExecutablePath == "" {
		spec.ExecutablePath = c.executable
	}
	if len(spec.AltCommands) == 0 {
		spec.AltCommands = append([]string(nil), c.alternatives...)
	}
	return spec
}

func compactPaths(paths ...string) []string {
	out := paths[:0]
	for _, path := range paths {
		if path != "" && path != "." {
			out = append(out, path)
		}
	}
	return out
}
