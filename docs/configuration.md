# Configuration

K0smotron managed control planes are managed using custom resource objects.

```yaml
apiVersion: k0smotron.io/v1beta1
kind: Cluster
metadata:
  name: k0smotron-test
spec:
  replicas: 1
  image: k0sproject/k0s
  version: v1.27.1-k0s.0
  service:
    type: NodePort
    apiPort: 30443
    konnectivityPort: 30132
  persistence:
    type: emptyDir
```

For full reference of the fields check out the [reference docs](resource-reference.md#cluster).

## Persistence

K0smotron persists data related to each Cluster. Specifically, it persists the `/var/lib/k0s` directory of the k0s controller which is the default data directory used by k0s. 

The `/var/lib/k0s` directory contains essential data for the operation of the k0s controller, but its growth over time is primarily driven by the addition of small [manifest](https://docs.k0sproject.io/stable/manifests/) files. Since these manifests are lightweight and in text format, the directory tends to grow gradually and not excessively. Typically, 250 MB of space is sufficient to handle its growth, as the main additions are these small manifests, keeping the overall size manageable.

The type of persistence used for this can be configurable via `spec.persistence`. For more information, check out the [reference docs](resource-reference.md/#clusterspecpersistence) on Cluster persistence.

## K0s configuration

K0smotron allows you to configure k0s via `spec.k0sConfig` field. This field expects a k0s **ClusterConfig** resource as value, which defines the configuration parameters for k0s. If `spec.k0sConfig` is left empty, the default k0s configuration will be applied.

Refer to [k0s docs](https://docs.k0sproject.io/stable/configuration/) for a reference on configuring k0s via the ClusterConfig resource.

### ClusterConfig for K0smotron

K0smotron can automatically generate `spec.k0sConfig` or override some fields (if provided) based on the values provided for the [Cluster](resource-reference.md/#clusterspec) resource, following specific configuration rules:

<table>
  <tr>
    <th style="width: 30%;">ClusterConfig Field</th>
    <th style="width: 30%;">Value</th>
    <th>Condition</th>
  </tr>
  <tr>
    <td><code>api.externalAddress</code></td>
    <td>Value in <code>spec.externalAddress</code> if provided. Otherwise, depending of the service type, K0smotron attempts to detect an external address from the load balancer or available node IPs.</td>
    <td>Only set if <code>nodeLocalLoadBalancing.enabled</code> is <code>false</code>.</td>
  </tr>
  <tr>
    <td><code>api.port</code></td>
    <td>Value in <code>spec.service.apiPort</code></td>
    <td>Always.</td>
  </tr>
  <tr>
    <td><code>api.sans</code></td>
    <td><code>[&lt;spec.externalAddress&gt;, &lt;cluster-svc-ips&gt;, &lt;cluster-service-name&gt;, &lt;cluster-service-name-namespaced&gt;, &lt;cluster-service-name-DNS&gt;], &lt;cluster-service-name-FQDNS&gt;</code> plus the possible provided ones.</td>
    <td>Always.</td>
  </tr>
  <tr>
    <td><code>konnectivity.port</code></td>
    <td>Value in <code>spec.service.konnectivityPort</code></td>
    <td>Always.</td>
  </tr>
  <tr>
    <td><code>storage.kine.dataSource</code></td>
    <td>Value in <code>spec.kineDataSourceURL</code></td>
    <td>Only set if <code>spec.kineDataSourceURL</code> is defined.</td>
  </tr>
  <tr>
    <td><code>storage.type</code></td>
    <td><code>kine</code></td>
    <td>Only set if <code>spec.kineDataSourceURL</code> is defined.</td>
  </tr>
  <tr>
    <td><code>storage.type</code></td>
    <td><code>etcd</code></td>
    <td>Only set if <code>spec.kineDataSourceURL</code> is not defined.</td>
  </tr>
  <tr>
    <td><code>storage.etcd.externalCluster.endpoints</code></td>
    <td><code>[https://kmc-&lt;cluster.name&gt;-etcd:2379]</code></td>
    <td>Only set if <code>spec.kineDataSourceURL</code> is not defined.</td>
  </tr>
  <tr>
    <td><code>storage.etcd.externalCluster.etcdPrefix</code></td>
    <td>Value in <code>metadata.name</code></td>
    <td>Only set if <code>spec.kineDataSourceURL</code> is not defined.</td>
  </tr>
  <tr>
    <td><code>storage.etcd.externalCluster.caFile</code></td>
    <td><code>/var/lib/k0s/pki/etcd-ca.crt</code></td>
    <td>Only set if <code>spec.kineDataSourceURL</code> is not defined.</td>
  </tr>
  <tr>
    <td><code>storage.etcd.externalCluster.clientCertFile</code></td>
    <td><code>/var/lib/k0s/pki/apiserver-etcd-client.crt</code></td>
    <td>Only set if <code>spec.kineDataSourceURL</code> is not defined.</td>
  </tr>
  <tr>
    <td><code>storage.etcd.externalCluster.clientKeyFile</code></td>
    <td><code>/var/lib/k0s/pki/apiserver-etcd-client.key</code></td>
    <td>Only set if <code>spec.kineDataSourceURL</code> is not defined.</td>
  </tr>
</table>
