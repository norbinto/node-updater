package configmap

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap/zaptest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestCreateConfigMap(t *testing.T) {
	logger := zaptest.NewLogger(t)
	kubeClient := fake.NewSimpleClientset()
	controller := NewConfigMapController(kubeClient, logger)

	err := controller.CreateConfigMap("default", "test-configmap", map[string]string{"key": "value"})
	if err != nil {
		t.Fatalf("CreateConfigMap failed: %v", err)
	}

	// Verify the ConfigMap was created
	_, err = kubeClient.CoreV1().ConfigMaps("default").Get(context.TODO(), "test-configmap", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Expected ConfigMap to be created, but it was not: %v", err)
	}
}

func TestCreateConfigMap_AlreadyExists(t *testing.T) {
	logger := zaptest.NewLogger(t)
	kubeClient := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-configmap",
			Namespace: "default",
		},
	})
	controller := NewConfigMapController(kubeClient, logger)

	err := controller.CreateConfigMap("default", "test-configmap", map[string]string{"key": "value"})
	if err != nil {
		t.Fatalf("CreateConfigMap failed: %v", err)
	}
}

func TestDeleteConfigMap(t *testing.T) {
	logger := zaptest.NewLogger(t)
	kubeClient := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-configmap",
			Namespace: "default",
		},
	})
	controller := NewConfigMapController(kubeClient, logger)

	err := controller.DeleteConfigMap("default", "test-configmap")
	if err != nil {
		t.Fatalf("DeleteConfigMap failed: %v", err)
	}

	// Verify the ConfigMap was deleted
	_, err = kubeClient.CoreV1().ConfigMaps("default").Get(context.TODO(), "test-configmap", metav1.GetOptions{})
	if err == nil {
		t.Fatalf("Expected ConfigMap to be deleted, but it still exists")
	}
}

func TestDeleteConfigMap_NotFound(t *testing.T) {
	logger := zaptest.NewLogger(t)
	kubeClient := fake.NewSimpleClientset()
	controller := NewConfigMapController(kubeClient, logger)

	err := controller.DeleteConfigMap("default", "nonexistent-configmap")
	if err != nil {
		t.Fatalf("DeleteConfigMap failed: %v", err)
	}
}

func TestGetConfigMapData(t *testing.T) {
	logger := zaptest.NewLogger(t)
	kubeClient := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-configmap",
			Namespace: "default",
		},
		Data: map[string]string{"key": "value"},
	})
	controller := NewConfigMapController(kubeClient, logger)

	data, err := controller.GetConfigMapData("default", "test-configmap")
	if err != nil {
		t.Fatalf("GetConfigMapData failed: %v", err)
	}
	if data["key"] != "value" {
		t.Fatalf("Expected data 'key: value', got: %v", data)
	}
}

func TestGetConfigMapData_NotFound(t *testing.T) {
	logger := zaptest.NewLogger(t)
	kubeClient := fake.NewSimpleClientset()
	controller := NewConfigMapController(kubeClient, logger)

	_, err := controller.GetConfigMapData("default", "nonexistent-configmap")
	if err == nil {
		t.Fatalf("Expected error for nonexistent ConfigMap, got nil")
	}
}

func TestCreateConfigMap_Error(t *testing.T) {
	logger := zaptest.NewLogger(t)
	kubeClient := fake.NewSimpleClientset()
	kubeClient.PrependReactor("create", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("mock create error")
	})
	controller := NewConfigMapController(kubeClient, logger)

	err := controller.CreateConfigMap("default", "test-configmap", map[string]string{"key": "value"})
	if err == nil || err.Error() != "failed to create ConfigMap: mock create error" {
		t.Fatalf("Expected mock create error, got: %v", err)
	}
}

func TestDeleteConfigMap_Error(t *testing.T) {
	logger := zaptest.NewLogger(t)
	kubeClient := fake.NewSimpleClientset()
	kubeClient.PrependReactor("delete", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("mock delete error")
	})
	controller := NewConfigMapController(kubeClient, logger)

	err := controller.DeleteConfigMap("default", "test-configmap")
	if err == nil || err.Error() != "failed to delete ConfigMap: mock delete error" {
		t.Fatalf("Expected mock delete error, got: %v", err)
	}
}
