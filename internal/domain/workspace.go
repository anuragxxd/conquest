package domain

// WorkspaceKind identifies the type of workspace backing for a run.
type WorkspaceKind string

const (
	// WorkspaceKindNone means the run uses ephemeral emptyDir volumes (default).
	WorkspaceKindNone WorkspaceKind = "none"

	// WorkspaceKindPVC means the run uses a shared PersistentVolumeClaim.
	WorkspaceKindPVC WorkspaceKind = "pvc"
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
}

// IsPVC returns true if the workspace is backed by a PersistentVolumeClaim.
func (w *WorkspaceBinding) IsPVC() bool {
	return w != nil && w.Kind == WorkspaceKindPVC
}
