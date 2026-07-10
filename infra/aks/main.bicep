targetScope = 'resourceGroup'

param clusterName string
param location string = resourceGroup().location
param kubernetesVersion string = ''
param systemNodeCount int = 1
param systemNodeVmSize string = 'Standard_D4s_v5'
param userNodeCount int = 3
param userNodeVmSize string = 'Standard_D8s_v5'
@allowed([
  'Ubuntu'
  'AzureLinux'
])
param userNodeOsSKU string = 'Ubuntu'
@allowed([
  'OCIContainer'
  'KataMshvVmIsolation'
  'KataVmIsolation'
])
param userNodeWorkloadRuntime string = 'OCIContainer'
param userNodeLabels object = {
  'perf.azure.com/node-role': 'workload'
}
param containerRegistryName string = ''
param containerRegistrySku string = 'Basic'

var resolvedContainerRegistryName = empty(containerRegistryName) ? 'acr${take(uniqueString(resourceGroup().id, clusterName), 18)}' : containerRegistryName
var acrPullRoleDefinitionId = '7f951dda-4ed3-4680-a7ca-43fe172d538d'

resource acr 'Microsoft.ContainerRegistry/registries@2023-07-01' = {
  name: resolvedContainerRegistryName
  location: location
  sku: {
    name: containerRegistrySku
  }
  properties: {
    adminUserEnabled: false
    publicNetworkAccess: 'Enabled'
  }
}

resource aks 'Microsoft.ContainerService/managedClusters@2025-05-01' = {
  name: clusterName
  location: location
  identity: {
    type: 'SystemAssigned'
  }
  properties: {
    dnsPrefix: clusterName
    kubernetesVersion: empty(kubernetesVersion) ? null : kubernetesVersion
    agentPoolProfiles: [
      {
        name: 'systempool'
        mode: 'System'
        count: systemNodeCount
        vmSize: systemNodeVmSize
        osType: 'Linux'
        type: 'VirtualMachineScaleSets'
      }
      {
        name: 'userpool'
        mode: 'User'
        count: userNodeCount
        vmSize: userNodeVmSize
        osType: 'Linux'
        osSKU: userNodeOsSKU
        type: 'VirtualMachineScaleSets'
        workloadRuntime: userNodeWorkloadRuntime
        nodeLabels: userNodeLabels
      }
    ]
    networkProfile: {
      networkPlugin: 'azure'
      networkPluginMode: 'overlay'
      networkDataplane: 'cilium'
      networkPolicy: 'cilium'
    }
  }
}

resource aksAcrPull 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(acr.id, clusterName, acrPullRoleDefinitionId)
  scope: acr
  properties: {
    principalId: aks.properties.identityProfile.kubeletidentity.objectId
    principalType: 'ServicePrincipal'
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', acrPullRoleDefinitionId)
  }
}

output clusterName string = aks.name
output containerRegistryName string = acr.name
output containerRegistryLoginServer string = acr.properties.loginServer
