package job

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap/zaptest"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestKillJobByPod_Success(t *testing.T) {
	logger := zaptest.NewLogger(t)
	kubeClient := fake.NewSimpleClientset(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-job",
			Namespace: "default",
		},
	})
	controller := NewJobController(kubeClient, logger)

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					Kind: "Job",
					Name: "test-job",
				},
			},
		},
	}

	err := controller.KillJobByPod(context.TODO(), pod)
	if err != nil {
		t.Fatalf("KillJobByPod failed: %v", err)
	}

	// Verify the job was deleted
	_, err = kubeClient.BatchV1().Jobs("default").Get(context.TODO(), "test-job", metav1.GetOptions{})
	if err == nil {
		t.Fatalf("Expected job to be deleted, but it still exists")
	}
}

func TestKillJobByPod_NoOwnerReferences(t *testing.T) {
	logger := zaptest.NewLogger(t)
	kubeClient := fake.NewSimpleClientset()
	controller := NewJobController(kubeClient, logger)

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
	}

	err := controller.KillJobByPod(context.TODO(), pod)
	if err == nil || err.Error() != "pod test-pod has no owner references" {
		t.Fatalf("Expected no owner references error, got: %v", err)
	}
}

func TestKillJobByPod_NoJobOwner(t *testing.T) {
	logger := zaptest.NewLogger(t)
	kubeClient := fake.NewSimpleClientset()
	controller := NewJobController(kubeClient, logger)

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					Kind: "ReplicaSet",
					Name: "test-replicaset",
				},
			},
		},
	}

	err := controller.KillJobByPod(context.TODO(), pod)
	if err == nil || err.Error() != "no job owner found for pod test-pod" {
		t.Fatalf("Expected no job owner error, got: %v", err)
	}
}

func TestKillJobByPod_DeleteError(t *testing.T) {
	logger := zaptest.NewLogger(t)
	kubeClient := fake.NewSimpleClientset()
	kubeClient.PrependReactor("delete", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("mock delete error")
	})
	controller := NewJobController(kubeClient, logger)

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					Kind: "Job",
					Name: "test-job",
				},
			},
		},
	}

	err := controller.KillJobByPod(context.TODO(), pod)
	if err == nil || err.Error() != "failed to delete job: mock delete error" {
		t.Fatalf("Expected mock delete error, got: %v", err)
	}
}
