package wdl

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"
)

const defaultWorkspaceRoot = "/workspace"

type Runtime struct {
	Version string
}

var (
	defaultVersion         = "92"
	supportedVersions      = []string{"92"}
	supportedWDLVersions   = []string{"1.0", "1.1"}
	sharedWorkspaceRootEnv = "CONQUEST_SHARED_WORKSPACE_ROOT"
)

func DefaultVersion() string {
	return defaultVersion
}

func SupportedVersions() []string {
	versions := make([]string, len(supportedVersions))
	copy(versions, supportedVersions)
	return versions
}

func SupportedWDLVersions() []string {
	versions := make([]string, len(supportedWDLVersions))
	copy(versions, supportedWDLVersions)
	return versions
}

func ResolveRuntime(requested string) (Runtime, error) {
	version := strings.TrimSpace(requested)
	if version == "" {
		version = defaultVersion
	}

	for _, supported := range supportedVersions {
		if version == supported {
			return Runtime{Version: version}, nil
		}
	}

	return Runtime{}, fmt.Errorf("unsupported cromwell version %q; supported versions: %s", version, strings.Join(supportedVersions, ", "))
}

func ValidateWorkflowVersion(version string) error {
	version = strings.TrimSpace(version)
	for _, supported := range supportedWDLVersions {
		if version == supported {
			return nil
		}
	}
	return fmt.Errorf("unsupported WDL version %q; supported versions: %s", version, strings.Join(supportedWDLVersions, ", "))
}

func SharedWorkspaceRoot() string {
	if root := strings.TrimSpace(os.Getenv(sharedWorkspaceRootEnv)); root != "" {
		return path.Clean(root)
	}
	return defaultWorkspaceRoot
}

func CromwellExecutionRoot() string {
	return path.Join(SharedWorkspaceRoot(), "cromwell-executions")
}

func IsSharedLocalPath(raw string) bool {
	_, ok := normalizeSharedLocalPath(SharedWorkspaceRoot(), raw)
	return ok
}

func SharedLocalPathFromURL(raw string) (string, bool) {
	return sharedLocalPathFromURL(SharedWorkspaceRoot(), raw)
}

func normalizeSharedLocalPath(root, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || !path.IsAbs(raw) {
		return "", false
	}

	cleanedRoot := path.Clean(root)
	cleaned := path.Clean(raw)
	if cleaned == cleanedRoot || strings.HasPrefix(cleaned, cleanedRoot+"/") {
		return cleaned, true
	}

	return "", false
}

func sharedLocalPathFromURL(root, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if strings.HasPrefix(raw, "file://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", false
		}
		if u.Host != "" && u.Host != "localhost" {
			return "", false
		}
		return normalizeSharedLocalPath(root, u.Path)
	}
	return normalizeSharedLocalPath(root, raw)
}
