package k8s

import "k8s.io/api/core/v1"

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
