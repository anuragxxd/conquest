package nextflow

import (
	"fmt"
	"strings"
)

// Plugin identifies a Nextflow plugin and its pinned version.
type Plugin struct {
	ID      string
	Version string
}

// Identifier returns the plugin identifier used by Nextflow.
func (p Plugin) Identifier() string {
	if p.Version == "" {
		return p.ID
	}
	return p.ID + "@" + p.Version
}

// Runtime describes the platform-supported Nextflow runtime policy for a
// specific engine version.
type Runtime struct {
	Version         string
	PlatformPlugins []Plugin
	AllowedPlugins  []string
}

var (
	defaultVersion        = "24.10.4"
	supportedOrder        = []string{"24.10.4", "25.04.3", "25.10.2", "25.10.4"}
	platformPluginsLegacy = []Plugin{
		{ID: "nf-ga4gh", Version: "1.3.0"},
		{ID: "nf-amazon", Version: "2.9.0"},
	}
	platformPlugins25_10 = []Plugin{
		{ID: "nf-ga4gh", Version: "1.4.0"},
		{ID: "nf-amazon", Version: "2.9.0"},
	}
	allowedPlugins = []string{
		"nf-ga4gh",
		"nf-amazon",
		"nf-schema",
		"nf-core-utils",
		"nf-fgbio",
		"nf-prov",
	}
	supportedRuntimes = map[string]Runtime{
		"24.10.4": {
			Version:         "24.10.4",
			PlatformPlugins: clonePlugins(platformPluginsLegacy),
			AllowedPlugins:  allowedPlugins,
		},
		"25.04.3": {
			Version:         "25.04.3",
			PlatformPlugins: clonePlugins(platformPluginsLegacy),
			AllowedPlugins:  allowedPlugins,
		},
		"25.10.2": {
			Version:         "25.10.2",
			PlatformPlugins: clonePlugins(platformPlugins25_10),
			AllowedPlugins:  allowedPlugins,
		},
		"25.10.4": {
			Version:         "25.10.4",
			PlatformPlugins: clonePlugins(platformPlugins25_10),
			AllowedPlugins:  allowedPlugins,
		},
	}
)

func clonePlugins(plugins []Plugin) []Plugin {
	result := make([]Plugin, len(plugins))
	copy(result, plugins)
	return result
}

// DefaultVersion returns the platform default Nextflow version.
func DefaultVersion() string {
	return defaultVersion
}

// SupportedVersions returns the curated list of Nextflow versions the control
// plane understands.
func SupportedVersions() []string {
	versions := make([]string, len(supportedOrder))
	copy(versions, supportedOrder)
	return versions
}

// ResolveRuntime returns the runtime policy for the requested version, or the
// platform default when the request omits workflow_engine_version.
func ResolveRuntime(requested string) (Runtime, error) {
	version := strings.TrimSpace(requested)
	if version == "" {
		version = defaultVersion
	}

	rt, ok := supportedRuntimes[version]
	if !ok {
		return Runtime{}, fmt.Errorf("unsupported nextflow version %q; supported versions: %s", version, strings.Join(supportedOrder, ", "))
	}
	rt.PlatformPlugins = clonePlugins(rt.PlatformPlugins)
	rt.AllowedPlugins = append([]string(nil), rt.AllowedPlugins...)

	return rt, nil
}
