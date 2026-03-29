package snakemake

import (
	"fmt"
	"strings"
)

// Runtime describes a resolved Snakemake execution environment.
type Runtime struct {
	Version string

	// LocalExecution is true when this Snakemake version does not support
	// the executor plugin interface (SM < 8). In this mode, all tasks run
	// locally inside the runner pod instead of being dispatched via TES.
	LocalExecution bool
}

var (
	defaultVersion    = "9.17.3"
	supportedVersions = []string{"7.32.4", "9.17.3"}
	supportedRuntimes = map[string]Runtime{
		"7.32.4": {Version: "7.32.4", LocalExecution: true},
		"9.17.3": {Version: "9.17.3", LocalExecution: false},
	}
)

func DefaultVersion() string {
	return defaultVersion
}

func SupportedVersions() []string {
	versions := make([]string, len(supportedVersions))
	copy(versions, supportedVersions)
	return versions
}

func ResolveRuntime(requested string) (Runtime, error) {
	version := strings.TrimSpace(requested)
	if version == "" {
		version = defaultVersion
	}

	rt, ok := supportedRuntimes[version]
	if !ok {
		return Runtime{}, fmt.Errorf("unsupported snakemake version %q; supported versions: %s", version, strings.Join(supportedVersions, ", "))
	}

	return rt, nil
}
