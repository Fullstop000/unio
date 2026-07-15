package driver

import (
	"os/exec"

	"github.com/Fullstop000/unio/errs"
)

// ResolveExecutable resolves the CLI a driver should spawn from an AgentSpec,
// trying spec.ExecutablePath first and then any AltCommands, in order. It
// returns the resolved path on success, or a not_installed AgentError naming the
// primary command when none is found on PATH.
//
// Drivers call this while probing and before opening a Session so a missing CLI
// surfaces as a clear not_installed error rather than an obscure spawn failure.
func ResolveExecutable(spec AgentSpec) (string, *AgentError) {
	primary := spec.ExecutablePath
	candidates := make([]string, 0, 1+len(spec.AltCommands))
	if primary != "" {
		candidates = append(candidates, primary)
	}
	candidates = append(candidates, spec.AltCommands...)
	if len(candidates) == 0 {
		return "", errs.NotInstalled("no executable configured on AgentSpec")
	}
	for _, c := range candidates {
		if path, err := exec.LookPath(c); err == nil {
			return path, nil
		}
	}
	// Report the primary (most meaningful) command name.
	name := primary
	if name == "" {
		name = candidates[0]
	}
	return "", errs.NotInstalledCmd(name)
}

// IsInstalled reports whether the AgentSpec executable or an alternative
// resolves on PATH.
func IsInstalled(spec AgentSpec) bool {
	_, err := ResolveExecutable(spec)
	return err == nil
}
