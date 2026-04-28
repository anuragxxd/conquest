package domain

// WorkspaceKind identifies the type of workspace backing for a run.
type WorkspaceKind string

const (
	// WorkspaceKindNone means the run uses ephemeral emptyDir volumes (default).
	WorkspaceKindNone WorkspaceKind = "none"

	// WorkspaceKindPVC means the run uses a shared PersistentVolumeClaim.
	WorkspaceKindPVC WorkspaceKind = "pvc"

	// WorkspaceKindObjectFS means the run uses a GeeseFS-backed S3 mount
	// via the Yandex S3 CSI driver. A static PV and PVC are created per run.
	WorkspaceKindObjectFS WorkspaceKind = "objectfs"
)

// WorkspaceAccessMode describes the PVC access mode for shared workspaces.
type WorkspaceAccessMode string

const (
	WorkspaceAccessModeRWX WorkspaceAccessMode = "RWX"
	WorkspaceAccessModeRWO WorkspaceAccessMode = "RWO"
)

// WorkspaceBinding captures the workspace identity for a run or task.
// It is persisted alongside the run/task so that later reconciliation and
// task creation can reference the same PVC without re-reading config.
type WorkspaceBinding struct {
	Kind       WorkspaceKind       `json:"kind"`
	ClaimName  string              `json:"claim_name,omitempty"`
	AccessMode WorkspaceAccessMode `json:"access_mode,omitempty"`
	PVName     string              `json:"pv_name,omitempty"`
}

// IsPVC returns true if the workspace is backed by a PersistentVolumeClaim.
func (w *WorkspaceBinding) IsPVC() bool {
	return w != nil && w.Kind == WorkspaceKindPVC
}

// IsObjectFS returns true if the workspace is backed by an S3-mounted volume.
func (w *WorkspaceBinding) IsObjectFS() bool {
	return w != nil && w.Kind == WorkspaceKindObjectFS
}

// RequiresCleanup returns true if the workspace has Kubernetes resources
// (PVC, PV) that must be deleted when the run reaches a terminal state.
func (w *WorkspaceBinding) RequiresCleanup() bool {
	return w.IsPVC() || w.IsObjectFS()
}

// IsShared returns true if the workspace provides a shared filesystem
// visible to both the runner and task pods (PVC or objectfs).
func (w *WorkspaceBinding) IsShared() bool {
	return w.IsPVC() || w.IsObjectFS()
}
