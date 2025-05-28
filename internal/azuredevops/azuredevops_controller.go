package azuredevops

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"go.uber.org/zap"
)

type AzureDevopsControllerInterface interface {
	DisableAgent(poolName, agentName string) error
	RemoveAgent(poolName, agentName string) error
}

type AzureDevopsController struct {
	httpClient       Doer
	logger           *zap.Logger
	OrganizationName string
	AccessToken      string
}

type Doer interface {
	Do(req *http.Request) (*http.Response, error)
	// NewRequest(method string, url string, body io.Reader) (*http.Request, error)
}

func NewAzureDevopsController(client Doer, organizationName string, accessToken string, logger *zap.Logger) *AzureDevopsController {
	return &AzureDevopsController{httpClient: client, OrganizationName: organizationName, AccessToken: accessToken, logger: logger}
}

func (c *AzureDevopsController) DisableAgent(poolName, agentName string) error {
	c.logger.Debug("Disabling agent", zap.String("organization", c.OrganizationName), zap.String("poolName", poolName), zap.String("agentName", agentName))
	// Get the pool ID from the pool name
	poolID, err := c.getPoolIDFromName(c.OrganizationName, poolName)
	if err != nil {
		c.logger.Error("Error getting pool ID", zap.Error(err), zap.String("organization", c.OrganizationName), zap.String("poolName", poolName))
		return fmt.Errorf("failed to get pool ID from name: %w", err)
	}

	// Construct the API URL to list agents
	url := fmt.Sprintf("https://dev.azure.com/%s/_apis/distributedtask/pools/%s/agents?api-version=7.1-preview.1", c.OrganizationName, strconv.Itoa(poolID))

	// Create the HTTP request
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Add headers
	req.SetBasicAuth("", c.AccessToken)

	// Send the request
	client := c.httpClient
	resp, err := client.Do(req)
	if err != nil {
		c.logger.Error("Error sending HTTP request", zap.Error(err), zap.String("organization", c.OrganizationName), zap.String("poolName", poolName), zap.String("agentName", agentName))
		return fmt.Errorf("failed to send HTTP request: %w", err)
	}
	defer resp.Body.Close()

	// Check the response status
	if resp.StatusCode != http.StatusOK {
		c.logger.Error("Failed to list agents", zap.Error(fmt.Errorf("unexpected status code")), zap.Int("statusCode", resp.StatusCode), zap.String("organization", c.OrganizationName), zap.String("poolName", poolName), zap.String("agentName", agentName))
		return fmt.Errorf("failed to list agents: status code %d", resp.StatusCode)
	}

	// Parse the response body
	var response struct {
		Value []struct {
			ID   json.Number `json:"id"`
			Name string      `json:"name"`
		} `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		c.logger.Error("Error decoding response body", zap.Error(err), zap.String("organization", c.OrganizationName), zap.String("poolName", poolName), zap.String("agentName", agentName))
		return fmt.Errorf("failed to decode response body: %w", err)
	}

	// Find the agent ID by name
	var agentID int = 0
	for _, agent := range response.Value {
		if agent.Name == agentName {
			id, err := agent.ID.Int64()
			if err != nil {
				c.logger.Error("Error converting agent ID to int", zap.Error(err), zap.String("organization", c.OrganizationName), zap.String("poolName", poolName), zap.String("agentName", agentName))
				return fmt.Errorf("failed to convert agent ID to int: %w", err)
			}
			agentID = int(id)
			break
		}
	}
	if agentID == 0 {
		c.logger.Error("Agent not found", zap.Error(fmt.Errorf("agent not found")), zap.String("organization", c.OrganizationName), zap.String("poolName", poolName), zap.String("agentName", agentName))
		return fmt.Errorf("agent with name '%s' not found", agentName)
	}

	// Construct the API URL to disable the agent
	url = fmt.Sprintf("https://dev.azure.com/%s/_apis/distributedtask/pools/%s/agents/%s?api-version=7.1-preview.1", c.OrganizationName, strconv.Itoa(poolID), strconv.Itoa(agentID))

	// Create the request payload
	payload := struct {
		ID      int  `json:"id"`
		Enabled bool `json:"enabled"`
	}{
		ID:      agentID,
		Enabled: false,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		c.logger.Error("Error marshalling request payload", zap.Error(err), zap.String("organization", c.OrganizationName), zap.String("poolName", poolName), zap.String("agentName", agentName))
		return fmt.Errorf("failed to marshal request payload: %w", err)
	}

	// Create the HTTP request
	req, err = http.NewRequest("PATCH", url, bytes.NewBuffer(body))
	if err != nil {
		c.logger.Error("Error creating HTTP PATCH request", zap.Error(err), zap.String("organization", c.OrganizationName), zap.String("poolName", poolName), zap.String("agentName", agentName))
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Add headers
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("", c.AccessToken)

	// Send the request
	resp, err = client.Do(req)
	if err != nil {
		c.logger.Error("Error sending HTTP PATCH request", zap.Error(err), zap.String("organization", c.OrganizationName), zap.String("poolName", poolName), zap.String("agentName", agentName))
		return fmt.Errorf("failed to send HTTP request: %w", err)
	}
	defer resp.Body.Close()

	// Check the response status
	if resp.StatusCode != http.StatusOK {
		c.logger.Error("Failed to disable agent", zap.Error(fmt.Errorf("unexpected status code")), zap.Int("statusCode", resp.StatusCode), zap.String("organization", c.OrganizationName), zap.String("poolName", poolName), zap.String("agentName", agentName))
		return fmt.Errorf("failed to disable agent: status code %d", resp.StatusCode)
	}

	c.logger.Debug("Agent successfully disabled", zap.String("organization", c.OrganizationName), zap.String("poolName", poolName), zap.String("agentName", agentName))
	return nil
}

func (c *AzureDevopsController) RemoveAgent(poolName, agentName string) error {
	c.logger.Debug("Removing agent", zap.String("organization", c.OrganizationName), zap.String("poolName", poolName), zap.String("agentName", agentName))
	// Get the pool ID from the pool name
	poolID, err := c.getPoolIDFromName(c.OrganizationName, poolName)
	if err != nil {
		c.logger.Error("Error getting pool ID", zap.Error(err), zap.String("organization", c.OrganizationName), zap.String("poolName", poolName))
		return fmt.Errorf("failed to get pool ID from name: %w", err)
	}

	// Construct the API URL to list agents
	url := fmt.Sprintf("https://dev.azure.com/%s/_apis/distributedtask/pools/%s/agents?api-version=7.1-preview.1", c.OrganizationName, strconv.Itoa(poolID))

	// Create the HTTP request
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		c.logger.Error("Error creating HTTP request", zap.Error(err), zap.String("organization", c.OrganizationName), zap.String("poolName", poolName), zap.String("agentName", agentName))
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Add headers
	req.SetBasicAuth("", c.AccessToken)

	// Send the request
	client := c.httpClient
	resp, err := client.Do(req)
	if err != nil {
		c.logger.Error("Error sending HTTP request", zap.Error(err), zap.String("organization", c.OrganizationName), zap.String("poolName", poolName), zap.String("agentName", agentName))
		return fmt.Errorf("failed to send HTTP request: %w", err)
	}
	defer resp.Body.Close()

	// Check the response status
	if resp.StatusCode != http.StatusOK {
		c.logger.Error("Failed to list agents", zap.Error(fmt.Errorf("unexpected status code")), zap.Int("statusCode", resp.StatusCode), zap.String("organization", c.OrganizationName), zap.String("poolName", poolName), zap.String("agentName", agentName))
		return fmt.Errorf("failed to list agents: status code %d", resp.StatusCode)
	}

	// Parse the response body
	var response struct {
		Value []struct {
			ID   json.Number `json:"id"`
			Name string      `json:"name"`
		} `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		c.logger.Error("Error decoding response body", zap.Error(err), zap.String("organization", c.OrganizationName), zap.String("poolName", poolName), zap.String("agentName", agentName))
		return fmt.Errorf("failed to decode response body: %w", err)
	}

	// Find the agent ID by name
	var agentID int = 0
	for _, agent := range response.Value {
		if agent.Name == agentName {
			id, err := agent.ID.Int64()
			if err != nil {
				c.logger.Error("Error converting agent ID to int", zap.Error(err), zap.String("organization", c.OrganizationName), zap.String("poolName", poolName), zap.String("agentName", agentName))
				return fmt.Errorf("failed to convert agent ID to int: %w", err)
			}
			agentID = int(id)
			break
		}
	}
	if agentID == 0 {
		c.logger.Error("Agent not found", zap.Error(fmt.Errorf("agent not found")), zap.String("organization", c.OrganizationName), zap.String("poolName", poolName), zap.String("agentName", agentName))
		return fmt.Errorf("agent with name '%s' not found", agentName)
	}

	// Construct the API URL to remove the agent
	url = fmt.Sprintf("https://dev.azure.com/%s/_apis/distributedtask/pools/%s/agents/%s?api-version=7.1-preview.1", c.OrganizationName, strconv.Itoa(poolID), strconv.Itoa(agentID))

	// Create the HTTP request
	req, err = http.NewRequest("DELETE", url, nil)
	if err != nil {
		c.logger.Error("Error creating HTTP DELETE request", zap.Error(err), zap.String("organization", c.OrganizationName), zap.String("poolName", poolName), zap.String("agentName", agentName))
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Add headers
	req.SetBasicAuth("", c.AccessToken)

	// Send the request
	resp, err = client.Do(req)
	if err != nil {
		c.logger.Error("Error sending HTTP DELETE request", zap.Error(err), zap.String("organization", c.OrganizationName), zap.String("poolName", poolName), zap.String("agentName", agentName))
		return fmt.Errorf("failed to send HTTP request: %w", err)
	}
	defer resp.Body.Close()

	// Check the response status
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		c.logger.Error("Failed to remove agent", zap.Error(fmt.Errorf("unexpected status code")), zap.Int("statusCode", resp.StatusCode), zap.String("organization", c.OrganizationName), zap.String("poolName", poolName), zap.String("agentName", agentName))
		return fmt.Errorf("failed to remove agent: status code %d", resp.StatusCode)
	}

	c.logger.Debug("Agent successfully removed", zap.String("organization", c.OrganizationName), zap.String("poolName", poolName), zap.String("agentName", agentName))
	return nil
}

func (c *AzureDevopsController) getPoolIDFromName(organization, poolName string) (int, error) {
	// Construct the API URL to list pools
	url := fmt.Sprintf("https://dev.azure.com/%s/_apis/distributedtask/pools?api-version=7.1-preview.1", organization)

	// Send the request
	client := c.httpClient

	// Create the HTTP request
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		c.logger.Error("Error creating HTTP request", zap.Error(err), zap.String("organization", organization), zap.String("poolName", poolName))
		return 0, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Add headers
	req.SetBasicAuth("", c.AccessToken)

	resp, err := client.Do(req)
	if err != nil {
		c.logger.Error("Error sending HTTP request", zap.Error(err), zap.String("organization", organization), zap.String("poolName", poolName))
		return 0, fmt.Errorf("failed to send HTTP request: %w", err)
	}
	defer resp.Body.Close()

	// Check the response status
	if resp.StatusCode != http.StatusOK {
		c.logger.Error("Failed to list pools", zap.Error(fmt.Errorf("unexpected status code")), zap.Int("statusCode", resp.StatusCode), zap.String("organization", organization), zap.String("poolName", poolName))
		return 0, fmt.Errorf("failed to list pools: status code %d", resp.StatusCode)
	}

	// Parse the response body
	var response struct {
		Value []struct {
			ID   json.Number `json:"id"`
			Name string      `json:"name"`
		} `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		c.logger.Error("Error decoding response body", zap.Error(err), zap.String("organization", organization), zap.String("poolName", poolName))
		return 0, fmt.Errorf("failed to decode response body: %w", err)
	}

	// Find the pool ID by name
	for _, pool := range response.Value {
		if pool.Name == poolName {
			id, err := pool.ID.Int64()
			if err != nil {
				c.logger.Error("Error converting pool ID to int", zap.Error(err), zap.String("organization", organization), zap.String("poolName", poolName))
				return 0, fmt.Errorf("failed to convert pool ID to int: %w", err)
			}
			return int(id), nil
		}
	}

	c.logger.Error("Pool not found", zap.Error(fmt.Errorf("pool not found")), zap.String("organization", organization), zap.String("poolName", poolName))
	return 0, fmt.Errorf("pool with name '%s' not found", poolName)
}
