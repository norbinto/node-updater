package pod

import (
	"context"
	"fmt"
	"io"
	"norbinto/node-updater/internal/azuredevops"
	job "norbinto/node-updater/internal/job"
	"strings"

	"slices"

	"go.uber.org/zap"

	safev1 "norbinto/node-updater/api/v1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type PodController struct {
	kubeClient            kubernetes.Interface
	azureDevopsController azuredevops.AzureDevopsControllerInterface
	jobController         *job.JobController
	logger                *zap.Logger
}

func NewPodController(kubeClient kubernetes.Interface, azureDevopsController azuredevops.AzureDevopsControllerInterface, jobController *job.JobController, logger *zap.Logger) *PodController {
	return &PodController{
		kubeClient:            kubeClient,
		azureDevopsController: azureDevopsController,
		jobController:         jobController,
		logger:                logger,
	}
}

func (c *PodController) EvictIdlePods(ctx context.Context, pods []corev1.Pod) error {
	c.logger.Debug("Starting eviction of idle pods", zap.Int("podCount", len(pods)))
	for _, pod := range pods {
		poolName, err := c.getPodsPool(ctx, pod.Name, pod.Namespace)
		if err != nil {
			c.logger.Error("Failed to get pod pool", zap.Error(err), zap.String("podName", pod.Name), zap.String("namespace", pod.Namespace))
			return err
		}
		c.logger.Debug("Processing pod", zap.String("podName", pod.Name), zap.String("namespace", pod.Namespace), zap.String("poolName", poolName))
		if err := c.azureDevopsController.DisableAgent(poolName, pod.Name); err != nil {
			c.logger.Error("Failed to disable agent in Azure DevOps", zap.Error(err), zap.String("podName", pod.Name), zap.String("namespace", pod.Namespace), zap.String("poolName", poolName))
			return err
		}
		c.logger.Debug("Disabled agent in Azure DevOps", zap.String("podName", pod.Name), zap.String("namespace", pod.Namespace), zap.String("poolName", poolName))
		c.logger.Debug("Removing agent from Azure DevOps", zap.String("podName", pod.Name), zap.String("poolName", poolName))
		if err := c.azureDevopsController.RemoveAgent(poolName, pod.Name); err != nil {
			c.logger.Error("Failed to remove agent from Azure DevOps", zap.Error(err), zap.String("podName", pod.Name), zap.String("poolName", poolName))
			return err
		}
		c.logger.Debug("Agent removed from Azure DevOps", zap.String("podName", pod.Name), zap.String("poolName", poolName))
		c.logger.Info("Starting to evict pod", zap.String("podName", pod.Name), zap.String("namespace", pod.Namespace))

		if err := c.jobController.KillJobByPod(ctx, pod); err != nil {
			c.logger.Error("Failed to kill job associated with pod", zap.Error(err), zap.String("podName", pod.Name), zap.String("namespace", pod.Namespace))
			return err
		}

		if err := c.KillPod(ctx, pod); err != nil {
			c.logger.Error("Failed to kill pod", zap.Error(err), zap.String("podName", pod.Name), zap.String("namespace", pod.Namespace))
			return err
		}

		c.logger.Debug("Job killed successfully", zap.String("podName", pod.Name), zap.String("namespace", pod.Namespace))

		c.logger.Debug("Pod eviction completed", zap.String("podName", pod.Name), zap.String("namespace", pod.Namespace))
	}

	c.logger.Debug("Finished eviction of idle pods")
	return nil
}

func (c *PodController) GetSafeToEvictPods(ctx context.Context, spec safev1.SafeEvictSpec) ([]corev1.Pod, error) {
	c.logger.Debug("Fetching safe-to-evict pods", zap.Any("spec", spec))
	// Create a label selector from the provided labels
	podList, err := c.kubeClient.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		c.logger.Error("Error listing pods", zap.Error(err))
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	// Filter pods that do not have the specified labels and are in the namespaces array
	var filteredPods []corev1.Pod
	for _, pod := range podList.Items {
		// Check if the pod's namespace is in the namespaces array
		if !slices.Contains(spec.Namespaces, pod.Namespace) {
			continue
		}

		// Check if the pod does not have all the specified labels with matching values
		for key, value := range spec.LabelSelector {
			if pod.Labels[key] != value && pod.Status.Phase == corev1.PodRunning {
				logs, err := c.fetchPodLogs(ctx, pod.Name, pod.Namespace)
				if err != nil {
					c.logger.Error("Failed to fetch pod logs", zap.Error(err), zap.String("podName", pod.Name), zap.String("namespace", pod.Namespace))
					continue
				}

				for _, line := range spec.LastLogLines {
					if strings.HasSuffix(logs, line) {
						filteredPods = append(filteredPods, pod)
						break
					}
				}
				continue
			}
		}
	}

	c.logger.Debug("Filtered pods based on SafeEvictSpec", zap.Int("filteredPodCount", len(filteredPods)))
	return filteredPods, nil
}

func (c *PodController) KillPod(ctx context.Context, pod corev1.Pod) error {
	// Delete the pod
	err := c.kubeClient.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
	if err != nil {
		c.logger.Error("Error deleting pod", zap.Error(err), zap.String("podName", pod.Name), zap.String("namespace", pod.Namespace))
		return fmt.Errorf("failed to delete pod '%s' in namespace %s: %w", pod.Name, pod.Namespace, err)
	}
	return nil
}

func (c *PodController) fetchPodLogs(ctx context.Context, podName, namespace string) (string, error) {
	c.logger.Debug("Fetching logs for pod", zap.String("podName", podName), zap.String("namespace", namespace))
	req := c.kubeClient.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{})

	// Execute the request and read the logs
	logStream, err := req.Stream(ctx)
	if err != nil {
		c.logger.Error("Error streaming logs from pod", zap.Error(err), zap.String("podName", podName), zap.String("namespace", namespace))
		return "", fmt.Errorf("failed to fetch logs for pod '%s' in namespace %s: %w", podName, namespace, err)
	}
	defer logStream.Close()

	// Read the logs from the stream
	logs, err := io.ReadAll(logStream)
	if err != nil {
		c.logger.Error("Error reading logs from stream", zap.Error(err), zap.String("podName", podName), zap.String("namespace", namespace))
		return "", fmt.Errorf("failed to read logs for pod '%s' in namespace %s: %w", podName, namespace, err)
	}
	c.logger.Debug("Successfully fetched logs for pod", zap.String("podName", podName), zap.String("namespace", namespace))
	return string(logs), nil
}

func (c *PodController) getPodsPool(ctx context.Context, podName, namespace string) (string, error) {
	// Get the pod details
	pod, err := c.kubeClient.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		c.logger.Error("Error getting pod details", zap.Error(err), zap.String("podName", pod.Name), zap.String("namespace", namespace))
		return "", fmt.Errorf("failed to get pod '%s' in namespace %s: %w", podName, namespace, err)
	}

	// Iterate through the pod's environment variables to find AZP_POOL
	for _, container := range pod.Spec.Containers {
		for _, envVar := range container.Env {
			if envVar.Name == "AZP_POOL" {
				return envVar.Value, nil
			}
		}
	}
	c.logger.Debug("AZP_POOL environment variable not found", zap.String("podName", podName), zap.String("namespace", namespace))
	return "", fmt.Errorf("environment variable AZP_POOL not found in pod '%s' in namespace %s", podName, namespace)
}
