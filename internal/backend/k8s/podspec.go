package k8s

import (
	v1 "k8s.io/api/core/v1"

	"github.com/anuragxxd/conquest/internal/domain"
)

func imagePullSecretRefs(names []string) []v1.LocalObjectReference {
	refs := make([]v1.LocalObjectReference, 0, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		refs = append(refs, v1.LocalObjectReference{Name: name})
	}
	return refs
}

// PodSecurityConfig controls the security context of runner and task pods.
// The zero value is the baseline profile: containers run as whatever user
// their image declares, and only PVC workspaces get an fsGroup.
type PodSecurityConfig struct {
	// Restricted makes every runner and task pod satisfy the restricted Pod
	// Security Standard: a fixed non-root UID/GID, seccomp RuntimeDefault, no
	// privilege escalation and all capabilities dropped. Executor images then
	// run as RunAsUser instead of their own USER, the same model as nf-core's
	// docker profile (`-u $(id -u):$(id -g)`).
	Restricted bool
	RunAsUser  int64 // UID for every container (restricted only)
	RunAsGroup int64 // primary GID and fsGroup (restricted only)
}

// podSecurityContext returns the pod-level security context for a runner or
// task pod.
func podSecurityContext(sec PodSecurityConfig, binding *domain.WorkspaceBinding) *v1.PodSecurityContext {
	if !sec.Restricted {
		return podSecurityContextForWorkspace(binding)
	}
	runAsNonRoot := true
	runAsUser := sec.RunAsUser
	runAsGroup := sec.RunAsGroup
	fsGroup := sec.RunAsGroup
	return &v1.PodSecurityContext{
		RunAsNonRoot:   &runAsNonRoot,
		RunAsUser:      &runAsUser,
		RunAsGroup:     &runAsGroup,
		FSGroup:        &fsGroup,
		SeccompProfile: &v1.SeccompProfile{Type: v1.SeccompProfileTypeRuntimeDefault},
	}
}

// containerSecurityContext returns the settings the restricted Pod Security
// Standard requires on every container, init containers included. Returns nil
// for the baseline profile.
func containerSecurityContext(sec PodSecurityConfig) *v1.SecurityContext {
	if !sec.Restricted {
		return nil
	}
	allowPrivilegeEscalation := false
	return &v1.SecurityContext{
		AllowPrivilegeEscalation: &allowPrivilegeEscalation,
		Capabilities:             &v1.Capabilities{Drop: []v1.Capability{"ALL"}},
	}
}

// automountServiceAccountToken is false for every runner and task pod. Only
// the server talks to the Kubernetes API. Runner and task pods run workflow
// code, which must not inherit the conquest ServiceAccount's rights to create
// and delete Jobs and PVCs.
func automountServiceAccountToken() *bool {
	automount := false
	return &automount
}

// podSecurityContextForWorkspace returns a PodSecurityContext with fsGroup set
// to 1000 when the workspace is PVC-backed. This ensures that files written to
// the shared PVC are group-readable/writable across runner and task pods
// regardless of the container image's default UID. Returns nil for emptydir
// (preserves current behavior).
func podSecurityContextForWorkspace(binding *domain.WorkspaceBinding) *v1.PodSecurityContext {
	if binding == nil || !binding.IsPVC() {
		return nil
	}
	var fsGroup int64 = 1000
	return &v1.PodSecurityContext{
		FSGroup: &fsGroup,
	}
}

func awsSecretEnvVars(secretName string) []v1.EnvVar {
	if secretName == "" {
		secretName = "storage-secrets"
	}
	return []v1.EnvVar{
		{
			Name: "AWS_ACCESS_KEY_ID",
			ValueFrom: &v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{
					LocalObjectReference: v1.LocalObjectReference{Name: secretName},
					Key:                  "AWS_ACCESS_KEY_ID",
				},
			},
		},
		{
			Name: "AWS_SECRET_ACCESS_KEY",
			ValueFrom: &v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{
					LocalObjectReference: v1.LocalObjectReference{Name: secretName},
					Key:                  "AWS_SECRET_ACCESS_KEY",
				},
			},
		},
	}
}
