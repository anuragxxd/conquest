package snakemake

import (
	"fmt"
	"strings"
)

type Runtime struct {
	Version string
}

var (
	defaultVersion    = "9.17.3"
	supportedVersions = []string{"9.17.3"}
	supportedRuntimes = map[string]Runtime{
		"9.17.3": {Version: "9.17.3"},
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
