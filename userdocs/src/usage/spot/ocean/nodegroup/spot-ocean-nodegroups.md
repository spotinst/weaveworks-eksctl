# Creating a Nodegroup

To create a new nodegroup, run:

```bash
eksctl create nodegroup \
  --cluster <cluster-name> \
  --nodegroup-name <nodegroup-name> \
  --spot-ocean
```

To create multiple nodegroups and have more control over the configuration, a config file can be used.

```yaml
# cluster.yaml
# A cluster with two Ocean nodegroups.
---
apiVersion: eksctl.io/v1alpha5
kind: ClusterConfig

metadata:
  # existing ocean cluster
  name: cluster-name
  region: us-west-2

nodeGroups:
- name: ocean-ng-1
  [... nodegroup standard fields; ssh, tags, etc.]

  # Enable Ocean integration and use all defaults.
  spotOcean: {}

- name: ocean-ng-2
  [... nodegroup standard fields; ssh, tags, etc.]

  # Enable Ocean integration with custom configuration.
  spotOcean:
    strategy:
      # Percentage of Spot instances that would spin up
      # from the desired capacity.
      spotPercentage: 100

      # Allow Ocean to utilize any available reserved
      # instances first before purchasing Spot instances.
      utilizeReservedInstances: true

      # Launch On-Demand instances in case of no Spot
      # instances available.
      fallbackToOnDemand: true

    autoScaler:
      # Spare resource capacity management enabling fast
      # assignment of Pods without waiting for new resources
      # to launch.
      headrooms:

        # Number of CPUs to allocate. CPUs are denoted
        # in millicores, where 1000 millicores = 1 vCPU.
      - cpuPerUnit: 2

        # Number of GPUs to allocate.
        gpuPerUnit: 0

        # Amount of memory (MB) to allocate.
        memoryPerUnit: 64

        # Number of units to retain as headroom, where
        # each unit has the defined CPU and memory.
        numOfUnits: 1

    compute:
      instanceTypes:
        # Instance types allowed in the Ocean cluster.
        # Cannot be configured if the blacklist is configured.
        whitelist: # OR blacklist
        - t2.large
        - c5.large
```

## Nodegroups Immutability
By design, AWS nodegroups are immutable. This means that if you need to change something like the AMI or the instance type of nodegroup, you would need to create a new nodegroup with the desired changes, move the load and delete the old one. Check [Deleting and draining](../../../managing-nodegroups.md#deleting-and-draining).
## Ocean VNGs
By using Ocean VNGs, those changes on AWS nodegroups are made for you automatically by only modifying the configuration of the spotOcean object of the nodegroup.
