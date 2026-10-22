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

func TestMapJobToStatus_StageOutFailure(t *testing.T) {
	tests := []struct {
		name      string
		exitCode  int32
		message   string
		wantPhase domain.State
		wantIn    string
	}{
		{"clean exit, stage-out ok", 0, `{"exit_code":0,"status":"ok"}`, domain.StateComplete, "executor completed"},
		{"clean exit, no message", 0, "", domain.StateComplete, "executor completed"},
		{"clean exit, unparseable message", 0, `{"exit_code":0,"sta`, domain.StateComplete, "executor completed"},
		{"clean exit, stage-out failed", 0, `{"exit_code":0,"status":"stage_out_error","error":"x509: certificate signed by unknown authority"}`, domain.StateSystemError, "x509"},
		{"clean exit, S3 client failed", 0, `{"exit_code":0,"status":"s3_client_error","error":"bad endpoint"}`, domain.StateSystemError, "s3_client_error"},
		{"executor failed, stage-out failed", 2, `{"exit_code":2,"status":"stage_out_error","error":"x509"}`, domain.StateExecutorError, "exited with code 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset(&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-1",
					Namespace: "services",
					Labels:    map[string]string{"job-name": "job-1"},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodSucceeded,
					ContainerStatuses: []corev1.ContainerStatus{{
						Name: "executor",
						State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
							ExitCode: tt.exitCode,
							Message:  tt.message,
						}},
					}},
				},
			})

			b := NewBackend(client, Config{Namespace: "services"}, nil)
			status, err := b.mapJobToStatus(context.Background(), &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "job-1", Namespace: "services"}})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if status.Phase != tt.wantPhase {
				t.Fatalf("phase = %s, want %s (reason %q)", status.Phase, tt.wantPhase, status.Reason)
			}
			if !strings.Contains(status.Reason, tt.wantIn) {
				t.Fatalf("reason = %q, want it to contain %q", status.Reason, tt.wantIn)
			}
			if status.ExitCode == nil || *status.ExitCode != tt.exitCode {
				t.Fatalf("exit code = %v, want %d", status.ExitCode, tt.exitCode)
			}
		})
	}
}
