package infra

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/Azure/aks-burner/internal/run"
)

const armParametersSchema = "https://schema.management.azure.com/schemas/2019-04-01/deploymentParameters.json#"

var validClusterNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,52}[a-z0-9])?$`)

type Requirements struct {
	Provider  string     `yaml:"provider"`
	NodePools []NodePool `yaml:"nodePools"`
}

type NodePool struct {
	Name            string            `yaml:"name" json:"name"`
	Mode            string            `yaml:"mode" json:"mode"`
	Count           int               `yaml:"count" json:"count"`
	VMSize          string            `yaml:"vmSize" json:"vmSize"`
	OSType          string            `yaml:"osType" json:"osType"`
	OSSKU           string            `yaml:"osSKU" json:"osSKU"`
	WorkloadRuntime string            `yaml:"workloadRuntime" json:"workloadRuntime"`
	Labels          map[string]string `yaml:"labels" json:"labels"`
	Taints          []string          `yaml:"taints" json:"taints"`
}

func ClusterName(suiteName string, override string) (string, error) {
	if override != "" {
		if !validClusterNamePattern.MatchString(override) {
			return "", fmt.Errorf("invalid cluster name %q", override)
		}
		return override, nil
	}
	derived := "aks" + strings.ReplaceAll(suiteName, "-", "")
	if len(derived) <= 54 {
		return derived, nil
	}
	sum := sha256.Sum256([]byte(derived))
	return derived[:45] + "-" + hex.EncodeToString(sum[:])[:8], nil
}

func ValidateNodePools(suiteName string, pools []NodePool, selectors []run.NodeSelectorRequirement) error {
	poolsByName := make(map[string]NodePool, len(pools))
	hasSystemPool := false
	for _, pool := range pools {
		if _, exists := pool.Labels["kubernetes.azure.com/os-sku"]; exists {
			return fmt.Errorf("suite %s pool %q labels must not set reserved label %q; use osSKU instead", suiteName, pool.Name, "kubernetes.azure.com/os-sku")
		}
		if _, exists := poolsByName[pool.Name]; exists {
			return fmt.Errorf("suite %s has duplicate pool %q", suiteName, pool.Name)
		}
		poolsByName[pool.Name] = pool
		if pool.Mode == "System" {
			hasSystemPool = true
		}
	}
	if !hasSystemPool {
		return fmt.Errorf("suite %s requires at least one system pool", suiteName)
	}
	for _, selector := range selectors {
		pool, exists := poolsByName[selector.Pool]
		if !exists {
			return fmt.Errorf("suite %s selector %s references missing pool %s", suiteName, selector.Name, selector.Pool)
		}
		if pool.Count < selector.MinNodes {
			return fmt.Errorf("suite %s selector %s requires %d nodes on pool %s, which has %d", suiteName, selector.Name, selector.MinNodes, pool.Name, pool.Count)
		}
		for key, value := range selector.Labels {
			actual := pool.Labels[key]
			if key == "kubernetes.azure.com/os-sku" {
				actual = pool.OSSKU
			}
			if actual != value {
				return fmt.Errorf("suite %s selector %s requires label %s=%s on pool %s", suiteName, selector.Name, key, value, pool.Name)
			}
		}
	}
	return nil
}

type parameterValue[T any] struct {
	Value T `json:"value"`
}

type parameterDocument struct {
	Schema         string `json:"$schema"`
	ContentVersion string `json:"contentVersion"`
	Parameters     struct {
		ClusterName             parameterValue[string]     `json:"clusterName"`
		KubernetesVersion       parameterValue[string]     `json:"kubernetesVersion"`
		NodePools               parameterValue[[]NodePool] `json:"nodePools"`
		DeployContainerRegistry parameterValue[bool]       `json:"deployContainerRegistry"`
	} `json:"parameters"`
}

func ParametersJSON(clusterName string, kubernetesVersion string, pools []NodePool, deployRegistry bool) ([]byte, error) {
	doc := parameterDocument{Schema: armParametersSchema, ContentVersion: "1.0.0.0"}
	doc.Parameters.ClusterName.Value = clusterName
	doc.Parameters.KubernetesVersion.Value = strings.TrimPrefix(kubernetesVersion, "v")
	doc.Parameters.NodePools.Value = pools
	doc.Parameters.DeployContainerRegistry.Value = deployRegistry
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
