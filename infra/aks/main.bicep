targetScope = 'resourceGroup'

param clusterName string
param location string = resourceGroup().location
param kubernetesVersion string = ''
type NodePool = {
  name: string
  mode: 'System' | 'User'
  count: int
  vmSize: string
  osType: 'Linux'
  osSKU: 'Ubuntu' | 'AzureLinux'
  workloadRuntime: 'OCIContainer' | 'KataMshvVmIsolation' | 'KataVmIsolation'
  labels: object
  taints: string[]
}
param nodePools NodePool[]
param containerRegistrySku string = 'Basic'
param deployContainerRegistry bool = true

var containerRegistryName = 'acr${take(uniqueString(resourceGroup().id, clusterName), 18)}'
var acrPullRoleDefinitionId = '7f951dda-4ed3-4680-a7ca-43fe172d538d'

resource acr 'Microsoft.ContainerRegistry/registries@2023-07-01' = if (deployContainerRegistry) {
  name: containerRegistryName
  location: location
  sku: {
    name: containerRegistrySku
  }
  properties: {
    adminUserEnabled: false
    publicNetworkAccess: 'Enabled'
  }
}

resource aks 'Microsoft.ContainerService/managedClusters@2025-09-02-preview' = {
  name: clusterName
  location: location
  identity: {
    type: 'SystemAssigned'
  }
  properties: {
    dnsPrefix: clusterName
    kubernetesVersion: empty(kubernetesVersion) ? null : kubernetesVersion
    agentPoolProfiles: [for pool in nodePools: {
      name: pool.name
      mode: pool.mode
      count: pool.count
      vmSize: pool.vmSize
      osType: pool.osType
      osSKU: pool.osSKU
      type: 'VirtualMachineScaleSets'
      workloadRuntime: pool.workloadRuntime
      nodeLabels: pool.labels
      nodeTaints: pool.taints
    }]
    networkProfile: {
      networkPlugin: 'azure'
      networkPluginMode: 'overlay'
      networkDataplane: 'cilium'
      networkPolicy: 'cilium'
    }
  }
}

resource aksAcrPull 'Microsoft.Authorization/roleAssignments@2022-04-01' = if (deployContainerRegistry) {
  name: guid(acr.id, clusterName, acrPullRoleDefinitionId)
  scope: acr
  properties: {
    principalId: aks.properties.identityProfile.kubeletidentity.objectId
    principalType: 'ServicePrincipal'
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', acrPullRoleDefinitionId)
  }
}

output clusterName string = aks.name
output containerRegistryName string = deployContainerRegistry ? acr.name : ''
output containerRegistryLoginServer string = deployContainerRegistry ? acr!.properties.loginServer : ''
