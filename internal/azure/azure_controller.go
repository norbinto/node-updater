package azure

import (
	"encoding/json"
	"net/http"
	azuredevops "norbinto/node-updater/internal/azuredevops"
	"strings"
	"time"

	"go.uber.org/zap"
)

type AzureController struct {
	httpClient azuredevops.Doer
	logger     *zap.Logger
}

func NewAzureController(client azuredevops.Doer, logger *zap.Logger) *AzureController {
	return &AzureController{httpClient: client, logger: logger}
}

func (c *AzureController) GetClusterInfo() (string, string, string, error) {
	const imdsURL = "http://169.254.169.254/metadata/instance?api-version=2021-02-01"
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	req, err := http.NewRequest("GET", imdsURL, nil)
	if err != nil {
		return "", "", "", err
	}

	req.Header.Set("Metadata", "true")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", "", err
	}

	var metadata struct {
		Compute struct {
			ResourceGroupName string `json:"resourceGroupName"`
			SubscriptionID    string `json:"subscriptionId"`
		} `json:"compute"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&metadata); err != nil {
		return "", "", "", err
	}

	// Extract cluster name from resourceGroupName
	parts := strings.Split(metadata.Compute.ResourceGroupName, "_")
	if len(parts) < 2 {
		return "", "", "", err
	}

	clusterName := parts[2]
	clusterResourceGroup := parts[1] // Assuming the cluster name is the second part
	return metadata.Compute.SubscriptionID, clusterResourceGroup, clusterName, err
}
