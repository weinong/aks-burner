using '../../infra/aks/main.bicep'

param clusterName = 'akskataio'
param kubernetesVersion = '1.36.1'
param userNodeCount = 4
param userNodeVmSize = 'Standard_D8s_v5'
param userNodeOsSKU = 'AzureLinux'
param userNodeWorkloadRuntime = 'KataMshvVmIsolation'
param userNodeLabels = {
  'perf.azure.com/node-role': 'workload'
}
