package job

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type JobController struct {
	kubeClient kubernetes.Interface
	logger     *zap.Logger
}

func NewJobController(kubeClient kubernetes.Interface, logger *zap.Logger) *JobController {
	return &JobController{
		kubeClient: kubeClient,
		logger:     logger,
	}
}

func (c *JobController) KillJobByPod(ctx context.Context, pod v1.Pod) error {
	c.logger.Debug("Attempting to kill job", zap.String("podName", pod.Name), zap.String("namespace", pod.Namespace))

	// Check if the pod has an owner reference (e.g., a job)
	if len(pod.OwnerReferences) == 0 {
		c.logger.Warn("Pod has no owner references", zap.String("podName", pod.Name))
		return fmt.Errorf("pod %s has no owner references", pod.Name)
	}

	// Find the owner reference of kind "Job"
	var jobName string
	for _, ownerRef := range pod.OwnerReferences {
		if strings.ToLower(ownerRef.Kind) == "job" {
			jobName = ownerRef.Name
			break
		}
	}

	if jobName == "" {
		c.logger.Warn("No job owner found for pod", zap.String("podName", pod.Name))
		return fmt.Errorf("no job owner found for pod %s", pod.Name)
	}

	// Delete the job
	err := c.kubeClient.BatchV1().Jobs(pod.Namespace).Delete(ctx, jobName, metav1.DeleteOptions{})
	if err != nil {
		c.logger.Error("Failed to delete job", zap.String("jobName", jobName), zap.Error(err))
		return fmt.Errorf("failed to delete job: %w", err)
	}

	c.logger.Debug("Successfully killed job", zap.String("jobName", jobName))
	return nil
}
