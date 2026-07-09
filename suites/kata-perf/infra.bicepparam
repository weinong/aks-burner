using '../../infra/aks/main.bicep'

param clusterName = 'akskataperf'
param containerRegistrySku = 'Basic'
param kubernetesVersion = '1.36'
param userNodeCount = 1
param userNodeVmSize = 'Standard_D16as_v5'
param userNodeOsSKU = 'AzureLinux'
param userNodeWorkloadRuntime = 'KataMshvVmIsolation'
param userNodeLabels = {
  'perf.azure.com/node-role': 'workload'
}
