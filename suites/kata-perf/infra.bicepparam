using '../../infra/aks/main.bicep'

param clusterName = 'akskataperf'
param userNodeCount = 3
param userNodeVmSize = 'Standard_D8s_v5'
param userNodeLabels = {
  'perf.azure.com/node-role': 'workload'
}
