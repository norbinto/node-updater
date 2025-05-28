package configmap

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type ConfigMapController struct {
	kubeClient kubernetes.Interface
	logger     *zap.Logger
}

func NewConfigMapController(kubeClient kubernetes.Interface, logger *zap.Logger) *ConfigMapController {
	return &ConfigMapController{
		kubeClient: kubeClient,
		logger:     logger,
	}
}

// EnsureConfigMap ensures that a ConfigMap exists in the specified namespace
func (c *ConfigMapController) CreateConfigMap(namespace string, name string, data map[string]string) error {
	_, err := c.getConfigMap(namespace, name)
	if err == nil {
		c.logger.Debug("ConfigMap already exists, data is not changed in it", zap.String("namespace", namespace), zap.String("name", name))
		return nil
	}

	configMap := &corev1.ConfigMap{
		ObjectMeta: v1.ObjectMeta{
			Name: name,
		},
		Data: data,
	}

	c.logger.Debug("Creating a new ConfigMap", zap.String("namespace", namespace), zap.String("name", name), zap.Any("data", data))
	_, err = c.kubeClient.CoreV1().ConfigMaps(namespace).Create(context.TODO(), configMap, v1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create ConfigMap: %v", err)
	}

	c.logger.Debug("ConfigMap created successfully", zap.String("namespace", namespace), zap.String("name", name))
	return nil

}

// DeleteConfigMap deletes a ConfigMap by name in the specified namespace
func (c *ConfigMapController) DeleteConfigMap(namespace string, name string) error {
	c.logger.Debug("Deleting ConfigMap", zap.String("namespace", namespace), zap.String("name", name))
	err := c.kubeClient.CoreV1().ConfigMaps(namespace).Delete(context.TODO(), name, v1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		c.logger.Debug("ConfigMap not found, nothing to delete", zap.String("namespace", namespace), zap.String("name", name))
		return nil
	}
	if err != nil {
		c.logger.Error("Failed to delete ConfigMap", zap.Error(err), zap.String("namespace", namespace), zap.String("name", name))
		return fmt.Errorf("failed to delete ConfigMap: %v", err)
	}
	c.logger.Debug("ConfigMap deleted successfully", zap.String("namespace", namespace), zap.String("name", name))
	return nil
}

// GetConfigMapData retrieves the data from a ConfigMap by name in the specified namespace
func (c *ConfigMapController) GetConfigMapData(namespace string, name string) (map[string]string, error) {
	c.logger.Debug("Retrieving ConfigMap data", zap.String("namespace", namespace), zap.String("name", name))
	configMap, err := c.getConfigMap(namespace, name)
	if apierrors.IsNotFound(err) {
		c.logger.Debug("ConfigMap not found, returning nil", zap.String("namespace", namespace), zap.String("name", name))
		return nil, err
	}
	if err != nil {
		c.logger.Error("Failed to get ConfigMap data", zap.Error(err), zap.String("namespace", namespace), zap.String("name", name))
		return nil, fmt.Errorf("failed to get ConfigMap data: %v", err)
	}

	c.logger.Debug("ConfigMap data retrieved successfully", zap.String("namespace", namespace), zap.String("name", name), zap.Any("data", configMap.Data))
	return configMap.Data, nil
}

// GetConfigMap retrieves a ConfigMap by name in the specified namespace
func (c *ConfigMapController) getConfigMap(namespace string, name string) (*corev1.ConfigMap, error) {
	configMap, err := c.kubeClient.CoreV1().ConfigMaps(namespace).Get(context.TODO(), name, v1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, err
	}

	if err != nil {
		c.logger.Error("Failed to get ConfigMap", zap.Error(err), zap.String("namespace", namespace), zap.String("name", name))
		return nil, fmt.Errorf("failed to get ConfigMap: %v", err)
	}

	return configMap, nil
}
