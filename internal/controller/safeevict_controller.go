/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"time"

	"norbinto/node-updater/internal/configmap"
	pod "norbinto/node-updater/internal/pod"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v2"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"

	"norbinto/node-updater/internal/appconfig"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	updatev1 "norbinto/node-updater/api/v1"
	nodepool "norbinto/node-updater/internal/nodepool"
)

// SafeEvictReconciler reconciles a SafeEvict object
type SafeEvictReconciler struct {
	client.Client
	Scheme              *runtime.Scheme
	KubeClient          kubernetes.Interface
	PodController       *pod.PodController
	ConfigmapController *configmap.ConfigMapController
	NodepoolController  *nodepool.NodePoolController
	Config              *appconfig.Config
	Logger              *zap.Logger
}

// var (
// 	saveEvictLog = ctrl.Log.WithName("safeEvict")
// )

// +kubebuilder:rbac:groups=update.norbinto,resources=safeevicts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=update.norbinto,resources=safeevicts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=update.norbinto,resources=safeevicts/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the SafeEvict object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.4/pkg/reconcile
func (c *SafeEvictReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	c.Logger.Info("Reconciling SafeEvict resource", zap.String("namespace", req.Namespace), zap.String("name", req.Name))

	// Fetch the SafeEvict instance
	safeEvict := &updatev1.SafeEvict{}
	err := c.Client.Get(ctx, req.NamespacedName, safeEvict)
	if err != nil {
		c.Logger.Error("Failed to get SafeEvict resource", zap.Error(err), zap.String("namespace", req.Namespace), zap.String("name", req.Name))
		return reconcile.Result{RequeueAfter: c.Config.ErrorReconcileTime}, client.IgnoreNotFound(err)
	}

	var outdatedNodes = make(map[string]corev1.Node)
	var outdatedNodePools = make(map[string]armcontainerservice.AgentPool)
	c.Logger.Debug("Checking if updates are needed for nodes and node pools...")
	//check if we need to update something
	outdatedNodes, outdatedNodePools, err = c.NodepoolController.UpdateNeeded(ctx, safeEvict.Spec.Nodepools)
	if err != nil {
		c.Logger.Error("Error determining if updates are needed for nodes and node pools", zap.Error(err))
		return reconcile.Result{RequeueAfter: c.Config.ErrorReconcileTime}, nil
	}

	notReadyPools, err := c.NodepoolController.GetNotReadyNodePools(ctx, safeEvict.Spec.Nodepools)
	if err != nil {
		c.Logger.Error("Failed to get not ready node pools", zap.Error(err))
		return reconcile.Result{RequeueAfter: c.Config.ErrorReconcileTime}, err
	}

	for poolName, pool := range notReadyPools {
		outdatedNodePools[poolName] = pool
	}

	c.Logger.Debug("Outdated nodes and node pools identified", zap.Int("outdatedNodes", len(outdatedNodes)), zap.Int("outdatedNodePools", len(outdatedNodePools)))
	c.Logger.Debug("Checking if temporary nodepool exists", zap.String("temporaryNodepoolName", safeEvict.GetTemporaryNodepoolName()))
	temporaryNodepoolExists, err := c.NodepoolController.NodePoolExists(ctx, safeEvict.GetTemporaryNodepoolName())
	if err != nil {
		c.Logger.Error("Failed to check if temporary nodepool exists", zap.Error(err))
		return reconcile.Result{RequeueAfter: c.Config.ErrorReconcileTime}, err
	}

	if !temporaryNodepoolExists {

		if len(outdatedNodes) == 0 && len(outdatedNodePools) == 0 {
			c.Logger.Debug("No outdated nodes or node pools found, deleting ConfigMap and requeuing...")
			err = c.ConfigmapController.DeleteConfigMap(req.Namespace, safeEvict.GetConfigmapName())
			if err != nil {
				c.Logger.Error("Failed to delete ConfigMap", zap.Error(err))
				return reconcile.Result{RequeueAfter: c.Config.ErrorReconcileTime}, err
			}
			c.Logger.Info(fmt.Sprintf("Cluster is up to date, requeuing for next reconciliation loop %d sec later", c.Config.UpgradeFrequency/time.Second))
			return reconcile.Result{RequeueAfter: c.Config.UpgradeFrequency}, nil
		}
		c.Logger.Info("Temporary nodepool does not exist and outdated nodes or node pools are found, creating temporary nodepool...")
		err = c.NodepoolController.CreateTemporaryNodePool(ctx, safeEvict.GetTemporaryNodepoolName(), safeEvict.Spec.BaseForBackupPool)
		if err != nil {
			c.Logger.Error("Failed to create temporary nodepool", zap.Error(err))
			return reconcile.Result{RequeueAfter: c.Config.ErrorReconcileTime}, nil
		}
	}

	// Check if the temporary node pool is still being created
	status, err := c.NodepoolController.GetNodePoolProvisioningState(ctx, safeEvict.GetTemporaryNodepoolName())
	if err != nil {
		return reconcile.Result{RequeueAfter: c.Config.ErrorReconcileTime}, err
	}
	//TODO: look for an enum
	if status == "Creating" {
		c.Logger.Info("Temporary node pool is being created, requeuing...")
		return reconcile.Result{RequeueAfter: c.Config.SuccessReconcileTime}, nil
	}

	configMapData, err := c.ConfigmapController.GetConfigMapData(req.Namespace, safeEvict.GetConfigmapName())
	if apierrors.IsNotFound(err) {
		configData := make(map[string]string)
		for poolName, pool := range outdatedNodePools {
			if pool.Properties.MinCount != nil || pool.Properties.MaxCount != nil {
				configData[poolName] = fmt.Sprintf(`{"MinCount": %d, "MaxCount": %d}`, *pool.Properties.MinCount, *pool.Properties.MaxCount)
			} else {
				configData[poolName] = fmt.Sprintf(`{"Count": %d}`, *pool.Properties.Count)
			}
		}
		c.Logger.Info("Creating ConfigMap with outdated node pool scaling information", zap.String("configMapName", safeEvict.GetConfigmapName()), zap.Any("data", configData))
		err = c.ConfigmapController.CreateConfigMap(req.Namespace, safeEvict.GetConfigmapName(), configData)
		if err != nil {
			c.Logger.Error("Failed to create ConfigMap with outdated node pool scaling information", zap.Error(err))
			return reconcile.Result{RequeueAfter: c.Config.ErrorReconcileTime}, err
		}
	} else {
		if err != nil {
			c.Logger.Error("Failed to retrieve ConfigMap data", zap.Error(err))
			return reconcile.Result{RequeueAfter: c.Config.ErrorReconcileTime}, err
		}
	}

	c.Logger.Debug("Starting to create evictions for outdated nodes and node pools...")
	err = c.performSafeEviction(ctx, outdatedNodePools, safeEvict)
	if err != nil {
		c.Logger.Error("Failed to perform safe eviction", zap.Error(err))
		return reconcile.Result{RequeueAfter: c.Config.ErrorReconcileTime}, err
	}
	c.Logger.Debug("Safe eviction process is ready")

	for _, nodepoolName := range safeEvict.Spec.Nodepools {
		c.Logger.Debug("Processing Nodepool", zap.String("nodepoolName", nodepoolName))
		nodes, err := c.NodepoolController.GetNodesByNodePool(ctx, nodepoolName)
		if err != nil {
			c.Logger.Error("Failed to get nodes by nodepool", zap.Error(err), zap.String("nodepoolName", nodepoolName))
			return reconcile.Result{RequeueAfter: c.Config.ErrorReconcileTime}, err
		}

		c.Logger.Debug("Checking for running stateful pods in the nodepool", zap.String("nodepoolName", nodepoolName), zap.Int("nodesCount", len(nodes)))
		// Check if any nodes in the nodepool still have pods running in the specified namespaces
		hasRunningPods, err := c.NodepoolController.HasRunningStatefulPods(ctx, nodes, safeEvict.Spec.Namespaces)
		if err != nil {
			c.Logger.Error("Error checking for running stateful pods in the nodepool", zap.Error(err), zap.String("nodepoolName", nodepoolName))
			return reconcile.Result{RequeueAfter: c.Config.ErrorReconcileTime}, err
		}
		if !hasRunningPods {
			c.Logger.Debug("No nodes in the nodepool still have running pods in the specified namespaces, updating node images...")

			nodepool, err := c.NodepoolController.GetNodePoolByName(ctx, nodepoolName)
			if err != nil {
				c.Logger.Error("Failed to get nodepool by name", zap.Error(err), zap.String("nodepoolName", nodepoolName))
				return reconcile.Result{RequeueAfter: c.Config.ErrorReconcileTime}, err
			}

			if nodepool.Properties != nil && nodepool.Properties.ProvisioningState != nil && *nodepool.Properties.ProvisioningState == "UpgradingNodeImageVersion" {
				c.Logger.Info(fmt.Sprintf("Node pool '%s' is still running a node image upgrade", *nodepool.Name))
				return reconcile.Result{RequeueAfter: c.Config.SuccessReconcileTime}, nil
			}

			c.Logger.Debug("Starting to upgrade node image version", zap.String("nodepoolName", nodepoolName))
			err = c.NodepoolController.UpgradeNodeImageVersion(ctx, nodepool)
			if err != nil {
				c.Logger.Error("Failed to upgrade node image version", zap.Error(err), zap.String("nodepoolName", nodepoolName))
				return reconcile.Result{RequeueAfter: c.Config.ErrorReconcileTime}, err
			}

		} else {
			if _, exists := outdatedNodePools[nodepoolName]; exists {
				c.Logger.Info(fmt.Sprintf("Nodepool '%s' still has running stateful pods", nodepoolName))
			}
		}
	}

	// if the nodepool is not outdated and cordoned, we should uncordon it
	for nodepoolName := range configMapData {
		if _, exists := outdatedNodePools[nodepoolName]; !exists {
			c.Logger.Debug("Nodepool is ready to take workload again", zap.String("nodepoolName", nodepoolName))
			nodepool, err := c.NodepoolController.GetNodePoolByName(ctx, nodepoolName)
			if err != nil {
				c.Logger.Error("Failed to get nodepool by name", zap.Error(err), zap.String("nodepoolName", nodepoolName))
				return reconcile.Result{RequeueAfter: c.Config.ErrorReconcileTime}, err
			}
			c.Logger.Debug("Restoring original scaling settings for the nodepool", zap.String("nodepoolName", nodepoolName), zap.String("scalingSettings", configMapData[nodepoolName]))
			err = c.NodepoolController.SetDefaultScaling(ctx, nodepool, configMapData[nodepoolName])
			if err != nil {
				if nodepool.Properties != nil && nodepool.Properties.ProvisioningState != nil && *nodepool.Properties.ProvisioningState == "Updating" {
					c.Logger.Debug(fmt.Sprintf("Node pool '%s' is still running a node image upgrade", *nodepool.Name))
					return reconcile.Result{RequeueAfter: c.Config.SuccessReconcileTime}, nil
				}
				c.Logger.Error("Failed to restore original scaling settings for the nodepool", zap.Error(err), zap.String("nodepoolName", nodepoolName))
				return reconcile.Result{RequeueAfter: c.Config.ErrorReconcileTime}, err
			}
			c.Logger.Debug("Restore of original scaling settings is completed", zap.String("nodepoolName", nodepoolName))
			c.Logger.Debug("Uncordoning nodes in the nodepool", zap.String("nodepoolName", nodepoolName))
			c.NodepoolController.CordonNodesByAgentPool(ctx, nodepoolName, false)
			c.Logger.Debug("Nodes in the nodepool have been uncordoned", zap.String("nodepoolName", nodepoolName))
		}
	}

	if len(outdatedNodes) == 0 && len(outdatedNodePools) == 0 {
		c.Logger.Info("All nodepools are up to date, cleaning up temporary resources")
		temporaryNodepool, err := c.NodepoolController.GetNodePoolByName(ctx, safeEvict.GetTemporaryNodepoolName())
		if err != nil && !apierrors.IsNotFound(err) {
			c.Logger.Error("Failed to get temporary nodepool by name", zap.Error(err), zap.String("temporaryNodepoolName", safeEvict.GetTemporaryNodepoolName()))
			return reconcile.Result{RequeueAfter: c.Config.ErrorReconcileTime}, err
		}

		temporaryNodepoolMap := map[string]armcontainerservice.AgentPool{
			*temporaryNodepool.Name: *temporaryNodepool,
		}
		c.Logger.Debug("Disabling auto-scaling for the temporary nodepool", zap.String("temporaryNodepoolName", safeEvict.GetTemporaryNodepoolName()))
		err = c.NodepoolController.DisableAutoScaling(ctx, temporaryNodepoolMap)
		if err != nil {
			c.Logger.Error("Failed to disable auto-scaling for the temporary nodepool", zap.Error(err), zap.String("temporaryNodepoolName", safeEvict.GetTemporaryNodepoolName()))
			return reconcile.Result{RequeueAfter: c.Config.ErrorReconcileTime}, err
		}

		temporaryNodes, err := c.NodepoolController.GetNodesByNodePool(ctx, *temporaryNodepool.Name)
		if err != nil {
			c.Logger.Error("Failed to get nodes by temporary nodepool", zap.Error(err), zap.String("temporaryNodepoolName", *temporaryNodepool.Name))
			return reconcile.Result{RequeueAfter: c.Config.ErrorReconcileTime}, err
		}

		temporaryNodesMap := make(map[string]corev1.Node)
		for _, node := range temporaryNodes {
			temporaryNodesMap[node.Name] = node
		}

		c.Logger.Debug("Starting to perform pod eviction from the temporary nodepool", zap.String("temporaryNodepoolName", *temporaryNodepool.Name))
		c.performSafeEviction(ctx, temporaryNodepoolMap, safeEvict)
		c.Logger.Debug("Pod evictions from the temporary nodepool are completed", zap.String("temporaryNodepoolName", *temporaryNodepool.Name))

		c.Logger.Debug("Checking for running stateful pods in the temporary nodepool", zap.String("temporaryNodepoolName", *temporaryNodepool.Name), zap.Int("nodesCount", len(temporaryNodes)))
		// Check if any nodes in the nodepool still have pods running in the specified namespaces
		hasRunningPods, err := c.NodepoolController.HasRunningStatefulPods(ctx, temporaryNodes, safeEvict.Spec.Namespaces)
		if err != nil {
			c.Logger.Error("Error checking for running stateful pods in the temporary nodepool", zap.Error(err), zap.String("temporaryNodepoolName", *temporaryNodepool.Name))
			return reconcile.Result{RequeueAfter: c.Config.ErrorReconcileTime}, err
		}
		if !hasRunningPods {
			c.Logger.Debug("All stateful pods have been evicted from the temporary nodepool,removing it...", zap.String("temporaryNodepoolName", *temporaryNodepool.Name))
			err = c.NodepoolController.RemoveTemporaryNodePool(ctx, safeEvict.GetTemporaryNodepoolName())
			if err != nil {
				c.Logger.Error("Failed to remove temporary nodepool", zap.Error(err), zap.String("temporaryNodepoolName", safeEvict.GetTemporaryNodepoolName()))
				return reconcile.Result{RequeueAfter: c.Config.ErrorReconcileTime}, nil
			}
			c.Logger.Info("Temporary nodepool has been removed successfully", zap.String("temporaryNodepoolName", safeEvict.GetTemporaryNodepoolName()))
			c.Logger.Debug("Starting to delete temporary ConfigMap", zap.String("configMapName", safeEvict.GetConfigmapName()))
			err = c.ConfigmapController.DeleteConfigMap(req.Namespace, safeEvict.GetConfigmapName())
			if err != nil {
				return reconcile.Result{RequeueAfter: c.Config.ErrorReconcileTime}, err
			}
			c.Logger.Info("ConfigMap deleted successfully", zap.String("configMapName", safeEvict.GetConfigmapName()))

		}
	}

	c.Logger.Info("Reconciliation loop completed", zap.String("namespace", req.Namespace), zap.String("name", req.Name))
	return reconcile.Result{RequeueAfter: c.Config.SuccessReconcileTime}, nil
}

func (c *SafeEvictReconciler) performSafeEviction(ctx context.Context, outdatedNodePools map[string]armcontainerservice.AgentPool, safeEvict *updatev1.SafeEvict) error {

	c.Logger.Debug("Disabling auto-scaling for node pools...")
	err := c.NodepoolController.DisableAutoScaling(ctx, outdatedNodePools)
	if err != nil {
		c.Logger.Error("Failed to disable auto-scaling for node pools", zap.Error(err))
		return err
	}

	for poolName, _ := range outdatedNodePools {
		err = c.NodepoolController.CordonNodesByAgentPool(ctx, poolName, true) //todo delete
		if err != nil {
			c.Logger.Error("Failed to cordon nodes", zap.Error(err))
			return err
		}

		safeToEvictPods, err := c.PodController.GetSafeToEvictPods(ctx, safeEvict.Spec)
		if err != nil {
			c.Logger.Error("Failed to get safe-to-evict pods", zap.Error(err))
			return err
		}
		nodes, err := c.NodepoolController.GetNodesByNodePool(ctx, poolName)
		if err != nil {
			c.Logger.Error("Failed to get safe-to-evict pods", zap.Error(err))
			return err
		}
		//only pods which runs on outdated nodes
		safeToEvictPods = filterPodsOnNodes(safeToEvictPods, nodes)

		err = c.PodController.EvictIdlePods(ctx, safeToEvictPods)
		if err != nil {
			c.Logger.Error("Failed to evict idle pods", zap.Error(err))
			return err
		}
	}

	c.Logger.Debug("Eviction process completed for safe-to-evict pods")
	return nil
}

func filterPodsOnNodes(safeToEvictPods []corev1.Pod, outdatedNodes []corev1.Node) []corev1.Pod {
	filteredPods := make([]corev1.Pod, 0)
	for _, pod := range safeToEvictPods {
		for _, node := range outdatedNodes {
			if pod.Spec.NodeName == node.Name {
				filteredPods = append(filteredPods, pod)
				break
			}
		}
	}
	return filteredPods
}

// SetupWithManager sets up the controller with the Manager.
func (r *SafeEvictReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&updatev1.SafeEvict{}).
		Named("safeevict").
		Complete(r)
}
