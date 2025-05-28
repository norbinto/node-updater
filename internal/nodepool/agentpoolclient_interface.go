package nodepool

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v2"
)

type AgentPoolClientInterface interface {
	Get(ctx context.Context, resourceGroup, clusterName, nodePoolName string, options *armcontainerservice.AgentPoolsClientGetOptions) (armcontainerservice.AgentPoolsClientGetResponse, error)
	BeginCreateOrUpdate(ctx context.Context, resourceGroup, clusterName, nodePoolName string, parameters armcontainerservice.AgentPool, options *armcontainerservice.AgentPoolsClientBeginCreateOrUpdateOptions) (*runtime.Poller[armcontainerservice.AgentPoolsClientCreateOrUpdateResponse], error)
	BeginDelete(ctx context.Context, resourceGroup, clusterName, nodePoolName string, options *armcontainerservice.AgentPoolsClientBeginDeleteOptions) (*runtime.Poller[armcontainerservice.AgentPoolsClientDeleteResponse], error)
	GetUpgradeProfile(ctx context.Context, resourceGroup, clusterName, nodePoolName string, options *armcontainerservice.AgentPoolsClientGetUpgradeProfileOptions) (armcontainerservice.AgentPoolsClientGetUpgradeProfileResponse, error)
	BeginUpgradeNodeImageVersion(ctx context.Context, resourceGroupName string, resourceName string, agentPoolName string, options *armcontainerservice.AgentPoolsClientBeginUpgradeNodeImageVersionOptions) (*runtime.Poller[armcontainerservice.AgentPoolsClientUpgradeNodeImageVersionResponse], error)
}
