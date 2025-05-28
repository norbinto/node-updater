package nodepool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"go.uber.org/zap"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	armcontainerservice "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v2"
)

type NodePoolController struct {
	kubeClient      kubernetes.Interface
	agentPoolClient AgentPoolClientInterface
	logger          *zap.Logger
}

func NewNodePoolController(kubeClient kubernetes.Interface, agentPoolClient AgentPoolClientInterface, logger *zap.Logger) *NodePoolController {
	return &NodePoolController{
		kubeClient:      kubeClient,
		agentPoolClient: agentPoolClient,
		logger:          logger,
	}
}

func (c *NodePoolController) UpdateNeeded(ctx context.Context, nodePools []string) (map[string]corev1.Node, map[string]armcontainerservice.AgentPool, error) {
	var outdatedNodes = make(map[string]corev1.Node)
	var outdatedNodePools = make(map[string]armcontainerservice.AgentPool)

	nodepoolNodeImageVersions, err := c.getNodeImageVersions(ctx, nodePools)
	if err != nil {
		c.logger.Error("Could not get node image versions for pools", zap.Error(err))
		return nil, nil, err
	}

	for nodepoolName, nodeImageVersion := range nodepoolNodeImageVersions {
		c.logger.Debug(fmt.Sprintf("Processing node pool '%s' with current image version '%s'", nodepoolName, nodeImageVersion))
		nodepoolLatestImageVersions, err := c.getNodePoolUpgradeProfile(ctx, nodepoolName)
		if err != nil {
			c.logger.Error("Failed to retrieve the latest node image version for node pool", zap.Error(err), zap.String("nodepoolName", nodepoolName))
			return nil, nil, err
		}
		nodes, err := c.GetNodesByNodePool(ctx, nodepoolName)
		if err != nil {
			c.logger.Error("Failed to retrieve the nodes for node pool", zap.Error(err), zap.String("nodepoolName", nodepoolName))
			return nil, nil, err
		}
		if nodeImageVersion != nodepoolLatestImageVersions {
			for _, node := range nodes {
				outdatedNodes[node.Name] = node
			}

			nodePool, err := c.GetNodePoolByName(ctx, nodepoolName)
			if err != nil {
				c.logger.Error("Failed to retrieve the node pool", zap.Error(err), zap.String("nodepoolName", nodepoolName))
				return nil, nil, err
			}
			outdatedNodePools[nodepoolName] = *nodePool
		}
		c.logger.Debug(fmt.Sprintf("Node pool '%s' has current image version '%s' and latest image version '%s'", nodepoolName, nodeImageVersion, nodepoolLatestImageVersions))
	}
	return outdatedNodes, outdatedNodePools, nil
}

func (c *NodePoolController) HasRunningStatefulPods(ctx context.Context, nodes []corev1.Node, namespaces []string) (bool, error) {
	for _, namespace := range namespaces {
		c.logger.Debug(fmt.Sprintf("Checking for running stateful pods in namespace '%s'", namespace))
		podList, err := c.kubeClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			c.logger.Error("Failed to list pods in namespace", zap.Error(err), zap.String("namespace", namespace))
			return false, err
		}
		c.logger.Debug(fmt.Sprintf("Found %d pods in namespace '%s'", len(podList.Items), namespace))
		for _, pod := range podList.Items {
			// Check if the pod is running and belongs to one of the specified nodes
			if pod.Status.Phase == corev1.PodRunning {
				for _, node := range nodes {
					if pod.Spec.NodeName == node.Name {
						c.logger.Info(fmt.Sprintf("Found running stateful pod '%s' on node '%s'", pod.Name, node.Name))
						return true, nil
					}
				}
			}
		}
	}
	c.logger.Debug("No running stateful pods found on the specified nodes in the given namespaces")
	return false, nil
}

func (c *NodePoolController) GetNodePoolByName(ctx context.Context, nodePoolName string) (*armcontainerservice.AgentPool, error) {
	// Get the node pool by name
	c.logger.Debug(fmt.Sprintf("Retrieving node pool '%s'", nodePoolName))
	nodePool, err := c.agentPoolClient.Get(ctx, os.Getenv("RESOURCE_GROUP"), os.Getenv("AKS_CLUSTER_NAME"), nodePoolName, nil)
	if apierrors.IsNotFound(err) {
		c.logger.Debug(fmt.Sprintf("Node pool '%s' not found", nodePoolName))
		return nil, err
	}
	if err != nil {
		c.logger.Error("Error occurred while getting node pool", zap.Error(err), zap.String("nodePoolName", nodePoolName))
		return nil, fmt.Errorf("unable to get node pool '%s': %v", nodePoolName, err)
	}
	c.logger.Debug(fmt.Sprintf("Successfully retrieved node pool '%s'", nodePoolName))
	return &nodePool.AgentPool, nil
}

func (c *NodePoolController) getNodeImageVersions(ctx context.Context, nodePoolNames []string) (map[string]string, error) {
	// List all nodes in the cluster
	nodeList := &corev1.NodeList{}
	nodeList, err := c.kubeClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		c.logger.Error("Failed to list nodes", zap.Error(err))
		return nil, fmt.Errorf("failed to list nodes: %v", err)
	}

	// Map to store node pool names and their node image versions
	nodeImageVersions := make(map[string]string)

	// Iterate through the nodes and group them by node pool
	for _, node := range nodeList.Items {
		// Extract the node pool name from the "agentpool" label
		nodePoolName, exists := node.Labels["agentpool"]
		if !exists {
			// Skip nodes without an "agentpool" label
			continue
		}

		// Check if the node pool name is in the nodePoolNames array
		found := false
		for _, name := range nodePoolNames {
			if name == nodePoolName {
				found = true
				break
			}
		}
		if !found {
			// Skip nodes that are not part of the specified node pools
			continue
		}

		// Extract the node image version from the "kubernetes.azure.com/node-image-version" label
		nodeImageVersion, exists := node.Labels["kubernetes.azure.com/node-image-version"]
		if !exists {
			// Skip nodes without a node image version label
			continue
		}

		// Add the node image version to the map if the node pool is not already present
		if _, found := nodeImageVersions[nodePoolName]; !found {
			nodeImageVersions[nodePoolName] = nodeImageVersion
		}
	}

	return nodeImageVersions, nil
}

func (c *NodePoolController) getNodePoolUpgradeProfile(ctx context.Context, nodePoolName string) (string, error) {

	// Call the API to get the upgrade profile for the specified node pool
	upgradeProfile, err := c.agentPoolClient.GetUpgradeProfile(ctx, os.Getenv("RESOURCE_GROUP"), os.Getenv("AKS_CLUSTER_NAME"), nodePoolName, nil)
	if err != nil {
		c.logger.Error("Failed to get upgrade profile for node pool", zap.Error(err), zap.String("nodePoolName", nodePoolName))
		return "", fmt.Errorf("unable to get upgrade profile for node pool '%s': %v", nodePoolName, err)
	}

	// Extract the latest node image version
	if upgradeProfile.Properties != nil && upgradeProfile.Properties.LatestNodeImageVersion != nil {
		c.logger.Debug(fmt.Sprintf("Latest node image version for node pool '%s' is '%s'", nodePoolName, *upgradeProfile.Properties.LatestNodeImageVersion))
		return *upgradeProfile.Properties.LatestNodeImageVersion, nil
	}

	return "", fmt.Errorf("latest node image version not available for node pool: %s", nodePoolName)
}

func (c *NodePoolController) GetNodesByNodePool(ctx context.Context, nodePoolName string) ([]corev1.Node, error) {
	c.logger.Debug(fmt.Sprintf("Retrieving nodes for node pool '%s'", nodePoolName))
	// List all nodes in the cluster
	nodeList := &corev1.NodeList{}
	nodeList, err := c.kubeClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		c.logger.Error("Failed to list nodes for node pool", zap.Error(err), zap.String("nodePoolName", nodePoolName))
		return nil, fmt.Errorf("failed to list nodes: %v", err)
	}

	// Slice to store nodes
	var nodes []corev1.Node

	// Iterate through the nodes and filter by the specified node pool
	for _, node := range nodeList.Items {
		// Check if the node belongs to the specified node pool
		if poolName, exists := node.Labels["agentpool"]; exists && poolName == nodePoolName {
			nodes = append(nodes, node)
		}
	}

	c.logger.Debug(fmt.Sprintf("Found %d nodes in node pool '%s'", len(nodes), nodePoolName))
	return nodes, nil
}

func (c *NodePoolController) CreateTemporaryNodePool(ctx context.Context, newNodePoolName string, sourceNodePoolName string) error {
	c.logger.Debug(fmt.Sprintf("Creating temporary node pool '%s' based on source node pool '%s'", newNodePoolName, sourceNodePoolName))

	// Get the source node pool configuration
	sourceNodePool, err := c.agentPoolClient.Get(ctx, os.Getenv("RESOURCE_GROUP"), os.Getenv("AKS_CLUSTER_NAME"), sourceNodePoolName, nil)
	if err != nil {
		c.logger.Error("Failed to get source node pool", zap.Error(err), zap.String("sourceNodePoolName", sourceNodePoolName))
		return fmt.Errorf("unable to get source node pool '%s': %v", sourceNodePoolName, err)
	}

	// Ensure the source node pool configuration is valid
	if sourceNodePool.Properties == nil {
		c.logger.Error("Invalid source node pool configuration", zap.Error(fmt.Errorf("source node pool '%s' has no properties", sourceNodePoolName)))
		return fmt.Errorf("source node pool '%s' has no properties", sourceNodePoolName)
	}

	// Create a new node pool configuration based on the source node pool
	newNodePool := armcontainerservice.AgentPool{
		Properties: &armcontainerservice.ManagedClusterAgentPoolProfileProperties{
			VMSize:              sourceNodePool.Properties.VMSize,
			Count:               sourceNodePool.Properties.Count,
			MinCount:            sourceNodePool.Properties.MinCount,
			MaxCount:            sourceNodePool.Properties.MaxCount,
			VnetSubnetID:        sourceNodePool.Properties.VnetSubnetID,
			Mode:                sourceNodePool.Properties.Mode,
			EnableAutoScaling:   sourceNodePool.Properties.EnableAutoScaling,
			OrchestratorVersion: sourceNodePool.Properties.OrchestratorVersion,
			NodeLabels:          sourceNodePool.Properties.NodeLabels,
			NodeTaints:          sourceNodePool.Properties.NodeTaints,
			OSType:              sourceNodePool.Properties.OSType,
		},
	}

	// Create the new node pool
	_, err = c.agentPoolClient.BeginCreateOrUpdate(ctx, os.Getenv("RESOURCE_GROUP"), os.Getenv("AKS_CLUSTER_NAME"), newNodePoolName, newNodePool, nil)
	if err != nil {
		c.logger.Error("Failed to create new node pool", zap.Error(err), zap.String("newNodePoolName", newNodePoolName))
		return fmt.Errorf("failed to create new node pool '%s': %v", newNodePoolName, err)
	}

	c.logger.Debug(fmt.Sprintf("Temporary node pool '%s' creation initiated successfully", newNodePoolName))
	return nil
}

func (c *NodePoolController) GetNodePoolProvisioningState(ctx context.Context, nodePoolName string) (string, error) {
	c.logger.Debug(fmt.Sprintf("Retrieving provisioning state for node pool '%s'", nodePoolName))
	// Get the node pool details
	nodePool, err := c.agentPoolClient.Get(ctx, os.Getenv("RESOURCE_GROUP"), os.Getenv("AKS_CLUSTER_NAME"), nodePoolName, nil)
	if err != nil {
		c.logger.Error("Error occurred while getting node pool", zap.Error(err), zap.String("nodePoolName", nodePoolName))
		return "", fmt.Errorf("unable to get node pool '%s': %v", nodePoolName, err)
	}

	// Check the provisioning state
	if nodePool.Properties != nil && nodePool.Properties.ProvisioningState != nil {
		c.logger.Debug(fmt.Sprintf("Provisioning state for node pool '%s' is '%s'", nodePoolName, *nodePool.Properties.ProvisioningState))
		return *nodePool.Properties.ProvisioningState, nil
	}

	c.logger.Error("Provisioning state not available", zap.Error(fmt.Errorf("provisioning state not available")), zap.String("nodePoolName", nodePoolName))
	return "", fmt.Errorf("provisioning state not available for node pool: %s", nodePoolName)
}

func (c *NodePoolController) NodePoolExists(ctx context.Context, nodePoolName string) (bool, error) {
	c.logger.Debug(fmt.Sprintf("Checking if node pool '%s' exists", nodePoolName))
	// Try to get the node pool
	_, err := c.agentPoolClient.Get(ctx, os.Getenv("RESOURCE_GROUP"), os.Getenv("AKS_CLUSTER_NAME"), nodePoolName, nil)
	if err != nil {
		// If the error indicates the node pool does not exist, return false
		var responseErr *azcore.ResponseError
		if errors.As(err, &responseErr) && responseErr.StatusCode == 404 {
			return false, nil
		}
		c.logger.Error("Error occurred while checking if node pool exists", zap.Error(err), zap.String("nodePoolName", nodePoolName))
		// For other errors, return the error
		return false, fmt.Errorf("error checking if node pool exists: %v", err)
	}

	c.logger.Debug(fmt.Sprintf("Node pool '%s' exists", nodePoolName))
	// If no error, the node pool exists
	return true, nil
}

func (c *NodePoolController) UpgradeNodeImageVersion(ctx context.Context, nodepool *armcontainerservice.AgentPool) error {
	c.logger.Debug(fmt.Sprintf("Starting node image version upgrade for node pool '%s'", *nodepool.Name))

	if nodepool.Properties != nil && nodepool.Properties.ProvisioningState != nil && (*nodepool.Properties.ProvisioningState == "UpgradingNodeImageVersion" || *nodepool.Properties.ProvisioningState == "Updating") {
		c.logger.Debug(fmt.Sprintf("Node pool '%s' is currently upgrading its node image version. Skipping further upgrade actions.", *nodepool.Name))
		return nil
	}

	nodepoolNodeImageVersions, err := c.getNodeImageVersions(ctx, []string{*nodepool.Name})
	if err != nil {
		c.logger.Error("Failed to get node image versions for node pool", zap.Error(err), zap.String("nodePoolName", *nodepool.Name))
		return err
	}
	nodepoolLatestImageVersions, err := c.getNodePoolUpgradeProfile(ctx, *nodepool.Name)
	if err != nil {
		c.logger.Error("Failed to retrieve the latest node image version for node pool", zap.Error(err), zap.String("nodePoolName", *nodepool.Name))
		return err
	}
	if nodepoolNodeImageVersions[*nodepool.Name] == nodepoolLatestImageVersions {
		c.logger.Debug(fmt.Sprintf("Node pool '%s' is already up to date. No upgrade needed.", *nodepool.Name))
		return nil
	}
	c.logger.Info(fmt.Sprintf("Node pool '%s' does not have the latest image version. Current: '%s', Latest: '%s'", *nodepool.Name, nodepoolNodeImageVersions[*nodepool.Name], nodepoolLatestImageVersions))
	c.logger.Info(fmt.Sprintf("Initiating node image version upgrade for node pool '%s'", *nodepool.Name))
	_, err = c.agentPoolClient.BeginUpgradeNodeImageVersion(ctx, os.Getenv("RESOURCE_GROUP"), os.Getenv("AKS_CLUSTER_NAME"), *nodepool.Name, nil)
	if err != nil {
		c.logger.Error("Failed to initiate node image version upgrade for node pool", zap.Error(err), zap.String("nodePoolName", *nodepool.Name))
		return fmt.Errorf("failed to upgrade node image version for node pool '%s': %v", *nodepool.Name, err)
	}

	c.logger.Debug(fmt.Sprintf("Node pool '%s' is upgrading to the latest node image version", *nodepool.Name))
	return nil
}

func (c *NodePoolController) DisableAutoScaling(ctx context.Context, agentPools map[string]armcontainerservice.AgentPool) error {
	for _, agentPool := range agentPools {
		// Skip processing if the agent pool is a system pool
		if agentPool.Properties != nil && agentPool.Properties.Mode != nil && *agentPool.Properties.Mode == armcontainerservice.AgentPoolModeSystem {
			c.logger.Debug(fmt.Sprintf("Skipping disabling autoscaling for system agent pool '%s'", *agentPool.Name))
			continue
		}

		if agentPool.Properties != nil && agentPool.Properties.Mode != nil && *agentPool.Properties.ProvisioningState != "Succeeded" {
			c.logger.Debug(fmt.Sprintf("Skipping disabling autoscaling for agent pool '%s' as its provisioning state is '%s'", *agentPool.Name, *agentPool.Properties.ProvisioningState))
			continue
		}

		// Ensure the agent pool has properties
		if agentPool.Properties == nil {
			c.logger.Error("Invalid agent pool configuration", zap.Error(fmt.Errorf("agent pool '%s' has no properties", *agentPool.Name)))
			return fmt.Errorf("agent pool '%s' has no properties", *agentPool.Name)
		}

		// Update the autoscaling setting
		agentPool.Properties.EnableAutoScaling = to.Ptr(false)

		c.logger.Debug(fmt.Sprintf("Disabling autoscaling for agent pool '%s'", *agentPool.Name))
		// Apply the update
		_, err := c.agentPoolClient.BeginCreateOrUpdate(ctx, os.Getenv("RESOURCE_GROUP"), os.Getenv("AKS_CLUSTER_NAME"), *agentPool.Name, agentPool, nil)
		if err != nil {
			var responseErr *azcore.ResponseError
			if errors.As(err, &responseErr) && responseErr.StatusCode == 409 {
				c.logger.Debug(fmt.Sprintf("Conflict error (409) encountered for agent pool '%s'. Reconciliation will be attempted.", *agentPool.Name))
				return nil
			}
			c.logger.Error("Failed to disable autoscaling for agent pool", zap.Error(err), zap.String("agentPoolName", *agentPool.Name))
			return fmt.Errorf("failed to update autoscaling for agent pool '%s': %v", *agentPool.Name, err)
		}
		c.logger.Debug(fmt.Sprintf("Autoscaling for agent pool '%s' has been successfully disabled", *agentPool.Name))
	}

	c.logger.Debug("Disabling autoscaling for agent pools completed")
	return nil
}

func (c *NodePoolController) RemoveTemporaryNodePool(ctx context.Context, nodePoolName string) error {
	// Delete the node pool
	c.logger.Debug(fmt.Sprintf("Starting to delete node pool '%s'", nodePoolName))
	_, err := c.agentPoolClient.BeginDelete(ctx, os.Getenv("RESOURCE_GROUP"), os.Getenv("AKS_CLUSTER_NAME"), nodePoolName, nil)
	if err != nil {
		c.logger.Error("Failed to delete node pool", zap.Error(err), zap.String("nodePoolName", nodePoolName))
		return fmt.Errorf("failed to delete node pool '%s': %v", nodePoolName, err)
	}
	c.logger.Debug(fmt.Sprintf("Node pool '%s' deletion initiated successfully", nodePoolName))
	return nil
}

func (c *NodePoolController) CordonNodesByAgentPool(ctx context.Context, nodePoolName string, toCordon bool) error {
	c.logger.Debug(fmt.Sprintf("Starting to uncordon nodes for agent pool '%s'", nodePoolName))

	nodes, err := c.GetNodesByNodePool(ctx, nodePoolName)
	if err != nil {
		return fmt.Errorf("failed to get nodes for agent pool '%s': %v", nodePoolName, err)
	}

	for _, node := range nodes {
		c.logger.Debug(fmt.Sprintf("Processing node '%s' for uncordoning", node.Name))
		// Check if the node is cordoned

		// Uncordon the node
		node.Spec.Unschedulable = toCordon
		_, err := c.kubeClient.CoreV1().Nodes().Update(ctx, &node, metav1.UpdateOptions{})
		if err != nil {
			c.logger.Error("Failed to set Unschedulable for node", zap.Error(err), zap.String("nodeName", node.Name), zap.Bool("toCordon", toCordon))
			return fmt.Errorf("failed to set Unschedulable for node '%s': %v", node.Name, err)
		}
		c.logger.Debug(fmt.Sprintf("Successfully set Unschedulable to '%t' for node '%s'", toCordon, node.Name))
	}

	c.logger.Debug(fmt.Sprintf("Successfully processed all nodes Unschedulable settings for agent pool '%s'", nodePoolName))
	return nil
}

func (c *NodePoolController) SetDefaultScaling(ctx context.Context, nodepool *armcontainerservice.AgentPool, scalingData string) error {

	if nodepool.Properties != nil && nodepool.Properties.Mode != nil && *nodepool.Properties.ProvisioningState != "Succeeded" {
		c.logger.Debug(fmt.Sprintf("Skipping scaling settings for agent pool '%s' as its provisioning state is '%s'", *nodepool.Name, *nodepool.Properties.ProvisioningState))
		return fmt.Errorf("node pool '%s' is still updating with provisioning state '%s'", *nodepool.Name, *nodepool.Properties.ProvisioningState)
	}

	c.logger.Debug(fmt.Sprintf("Setting default scaling configuration for node pool '%s'", *nodepool.Name))

	// Parse the scalingData JSON
	var scalingConfig map[string]int
	err := json.Unmarshal([]byte(scalingData), &scalingConfig)
	if err != nil {
		c.logger.Error("Failed to unmarshal scalingData JSON", zap.Error(err))
		return fmt.Errorf("failed to parse scalingData JSON: %v", err)
	}

	// Check if MinCount and MaxCount are present in the JSON
	minCount, hasMinCount := scalingConfig["MinCount"]
	maxCount, hasMaxCount := scalingConfig["MaxCount"]
	count, hasCount := scalingConfig["Count"]

	if hasMinCount && hasMaxCount {
		// Check if the current scaling configuration matches the desired configuration
		if nodepool.Properties.EnableAutoScaling != nil &&
			*nodepool.Properties.EnableAutoScaling &&
			nodepool.Properties.MinCount != nil &&
			nodepool.Properties.MaxCount != nil &&
			*nodepool.Properties.MinCount == int32(minCount) &&
			*nodepool.Properties.MaxCount == int32(maxCount) {
			c.logger.Debug(fmt.Sprintf("Node pool '%s' already has autoscaling enabled with MinCount: %d, MaxCount: %d", *nodepool.Name, minCount, maxCount))
			return nil
		}
		// Enable autoscaling and set MinCount and MaxCount
		nodepool.Properties.EnableAutoScaling = to.Ptr(true)
		nodepool.Properties.MinCount = to.Ptr(int32(minCount))
		nodepool.Properties.MaxCount = to.Ptr(int32(maxCount))
		c.logger.Debug(fmt.Sprintf("Autoscaling enabled for node pool '%s' with MinCount: %d, MaxCount: %d", *nodepool.Name, minCount, maxCount))
	} else if hasCount {
		// Disable autoscaling and set Count
		if nodepool.Properties.EnableAutoScaling != nil &&
			!*nodepool.Properties.EnableAutoScaling &&
			nodepool.Properties.Count != nil &&
			*nodepool.Properties.Count == int32(count) {
			c.logger.Debug(fmt.Sprintf("Node pool '%s' has been set to manual scaling set with Count: %d", *nodepool.Name, count))
			return nil
		}
		nodepool.Properties.EnableAutoScaling = to.Ptr(false)
		nodepool.Properties.Count = to.Ptr(int32(count))
		c.logger.Debug(fmt.Sprintf("Manual scaling set for node pool '%s' with Count: %d", *nodepool.Name, count))
	} else {
		c.logger.Error("ScalingData JSON must contain either MinCount and MaxCount or Count", zap.Error(fmt.Errorf("invalid scalingData JSON")))
	}

	c.logger.Debug(fmt.Sprintf("Applying scaling configuration for node pool '%s'", *nodepool.Name))
	// Apply the update

	_, err = c.agentPoolClient.BeginCreateOrUpdate(ctx, os.Getenv("RESOURCE_GROUP"), os.Getenv("AKS_CLUSTER_NAME"), *nodepool.Name, *nodepool, nil)
	if err != nil {
		var responseErr *azcore.ResponseError
		if errors.As(err, &responseErr) && responseErr.StatusCode == 409 {
			c.logger.Debug(fmt.Sprintf("Conflict error (409) encountered for agent pool '%s'. Reconciliation will be attempted.", *nodepool.Name))
			return nil
		}
		c.logger.Error("Failed to update scaling for node pool", zap.Error(err), zap.String("nodePoolName", *nodepool.Name))
		return fmt.Errorf("failed to update scaling for node pool '%s': %v", *nodepool.Name, err)
	}

	c.logger.Debug(fmt.Sprintf("Scaling configuration successfully updated for node pool '%s'", *nodepool.Name))
	return nil
}

func (c *NodePoolController) GetNotReadyNodePools(ctx context.Context, nodepools []string) (map[string]armcontainerservice.AgentPool, error) {
	notReadyNodePools := make(map[string]armcontainerservice.AgentPool)

	for _, nodepoolName := range nodepools {
		c.logger.Debug(fmt.Sprintf("Checking readiness of node pool '%s'", nodepoolName))

		nodePool, err := c.GetNodePoolByName(ctx, nodepoolName)
		if err != nil {
			c.logger.Error("Failed to retrieve node pool", zap.Error(err), zap.String("nodepoolName", nodepoolName))
			return nil, fmt.Errorf("failed to retrieve node pool '%s': %v", nodepoolName, err)
		}

		if nodePool.Properties != nil && nodePool.Properties.ProvisioningState != nil && *nodePool.Properties.ProvisioningState != "Succeeded" {
			c.logger.Debug(fmt.Sprintf("Node pool '%s' is not in a ready state. Current provisioning state: '%s'", nodepoolName, *nodePool.Properties.ProvisioningState))
			notReadyNodePools[nodepoolName] = *nodePool
		}
	}

	return notReadyNodePools, nil
}
