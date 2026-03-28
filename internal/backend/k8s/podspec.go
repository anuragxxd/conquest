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
