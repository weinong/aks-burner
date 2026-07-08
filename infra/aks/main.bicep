targetScope = 'resourceGroup'

param clusterName string
param location string = resourceGroup().location
param kubernetesVersion string = ''
param systemNodeCount int = 1
param systemNodeVmSize string = 'Standard_D4s_v5'
param userNodeCount int = 3
param userNodeVmSize string = 'Standard_D8s_v5'
param userNodeLabels object = {
  'perf.azure.com/node-role': 'workload'
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
        type: 'VirtualMachineScaleSets'
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

output clusterName string = aks.name
