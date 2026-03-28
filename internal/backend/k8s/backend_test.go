package k8s

import (
	"context"
	"strings"
	"testing"

	"github.com/anuragxxd/conquest/internal/domain"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestMapJobToStatus_TreatsInvalidImageNameAsTerminal(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-1",
			Namespace: "services",
			Labels:    map[string]string{"job-name": "job-1"},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "executor",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "InvalidImageName"}},
			}},
		},
	})

	b := NewBackend(client, Config{Namespace: "services"}, nil)
	status, err := b.mapJobToStatus(context.Background(), &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "job-1", Namespace: "services"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Phase != domain.StateSystemError {
		t.Fatalf("phase = %s, want %s", status.Phase, domain.StateSystemError)
	}
	if !strings.Contains(status.Reason, "InvalidImageName") {
		t.Fatalf("reason = %q, want InvalidImageName", status.Reason)
	}
}

func TestMapJobToStatus_TreatsInitContainerCreateErrorAsTerminal(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-1",
			Namespace: "services",
			Labels:    map[string]string{"job-name": "job-1"},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{{
				Name:  "stage-in",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CreateContainerConfigError"}},
			}},
		},
	})

	b := NewBackend(client, Config{Namespace: "services"}, nil)
	status, err := b.mapJobToStatus(context.Background(), &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "job-1", Namespace: "services"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Phase != domain.StateSystemError {
		t.Fatalf("phase = %s, want %s", status.Phase, domain.StateSystemError)
	}
	if !strings.Contains(status.Reason, "CreateContainerConfigError") {
		t.Fatalf("reason = %q, want CreateContainerConfigError", status.Reason)
	}
}
