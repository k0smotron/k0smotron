# Etcd configuration

k0smotron in HCP mode deploys an etcd cluster to store the state of the control plane.
The etcd cluster is deployed as a StatefulSet with odd number of replicas to ensure quorum.
For example:

| Control Plane replicas | etcd replicas |
|------------------------|---------------|
| 1                      | 1             |
| 2                      | 3             |
| 3                      | 3             |
| 4                      | 5             |
| 5                      | 5             |
| …                      | …             |

## Default configuration

The etcd cluster is deployed with the following default configuration options:

| etcd flag                       | default value |
|---------------------------------|---------------|
| --auto-compaction-mode=periodic | periodic      |
| --auto-compaction-retention     | 5m            |
| --snapshot-count                | 10000         |

## Customizing etcd configuration

k0smotron supports passing (and overriding default) etcd configuration flags to the etcd cluster by setting the `spec.etcd.args` field in the `Cluster` resource. For example:

```yaml
apiVersion: k0smotron.io/v1beta1
kind: Cluster
metadata:
  name: k0smotron-test
spec:
  replicas: 3
  etcd:
    args:
      - --auto-compaction-mode=periodic
      - --auto-compaction-retention=15m
      - --snapshot-count=20000
```

## Defragmentation

k0smotron supports running etcd defragmentation job periodically. The defragmentation job is disabled by default.
To enable the defragmentation job, set the `spec.etcd.defragJob.enabled` field to `true` in the `Cluster` resource.
If enabled, by default the defragmentation job runs every day at 12:00. You can customize the schedule and the rule for the defragmentation job by setting the `spec.etcd.defragJob.schedule` and `spec.etcd.defragJob.rule` fields in the `Cluster` resource. For example:

The feature is based on the **etcd-defrag** tool. For more information, see the [etcd-defrag repo](https://github.com/ahrtr/etcd-defrag).

```yaml
apiVersion: k0smotron.io/v1beta1
kind: Cluster
metadata:
  name: k0smotron-test
spec:
  etcd:
    defragJob:
      enabled: true
      schedule: "0 0 * * *" # Default: 0 12 * * *
      rule: "dbQuotaUsage > 0.5 || dbSize - dbSizeInUse > 200*1024*1024" # Default: dbQuotaUsage > 0.8 || dbSize - dbSizeInUse > 200*1024*1024
```

## Resource Requirements

k0smotron supports setting resource requirements (requests and limits) for the etcd StatefulSet pods. By default, etcd pods are created with no specific resource requirements. To set resource requirements, use the `spec.etcd.resources` field in the `Cluster` resource:

```yaml
apiVersion: k0smotron.io/v1beta1
kind: Cluster
metadata:
  name: k0smotron-test
spec:
  etcd:
    image: quay.io/k0sproject/etcd:v3.5.13
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
      limits:
        cpu: 200m
        memory: 256Mi
```

This configuration allows you to ensure etcd has appropriate resources allocated, which is especially important for production environments or clusters with high load. The resource requirements follow standard Kubernetes resource specification format.
