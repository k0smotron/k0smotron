# Update hosted control plane in Cluster API integrated cluster

To update k0smotron cluster deployed with Cluster API, you need to update
the k0s version and machine names in the YAML configuration file:

1. Localize the configuration of deployed k0smotron cluster in your repository. For example:

    ```yaml 
    apiVersion: cluster.x-k8s.io/v1beta1
    kind: Cluster
    metadata:
      name: docker-test
      namespace: default
    spec:
      clusterNetwork:
        pods:
          cidrBlocks:
          - 192.168.0.0/16
        serviceDomain: cluster.local
        services:
          cidrBlocks:
          - 10.128.0.0/12
      controlPlaneRef:
        apiVersion: controlplane.cluster.x-k8s.io/v1beta1
        kind: K0smotronControlPlane
        name: docker-test-cp
      infrastructureRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        kind: DockerCluster
        name: docker-test
    ---
    apiVersion: controlplane.cluster.x-k8s.io/v1beta1
    kind: K0smotronControlPlane
    metadata:
      name: docker-test-cp
    spec:
      version: v1.27.2-k0s.0
    ```
2. Make sure that the [persistence](https://docs.k0smotron.io/stable/resource-reference/#clusterspecpersistence) is configured
to prevent data loss. For example:

   ```yaml
    ---
    apiVersion: controlplane.cluster.x-k8s.io/v1beta1
    kind: K0smotronControlPlane
    metadata:
      name: docker-test-cp
    spec:
      version: v1.27.2-k0s.0
      persistence:
        type: hostPath
        hostPath: "/tmp/kmc-test" # k0smotron will mount a basic hostPath volume to avoid data loss.
   ```

   Using the `hostPath` volume type introduces many security risks.
   Avoid configuring persistence for volumes of the `hostPath` type in production environments.
   Learn more from [official Kubernetes documentation: hostPath](https://kubernetes.io/docs/concepts/storage/volumes/#hostpath).

3. Change all the k0s versions to the target one. For example:

   ```yaml
   apiVersion: controlplane.cluster.x-k8s.io/v1beta1
   kind: K0smotronControlPlane
   metadata:
     name: cp-test
   spec:
     version: v1.28.7-k0s.0 # new k0s version
   ```

4. Create a new version of the K0sWorkerConfigTemplate For example:

   ```yaml
   ---
    apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
    kind: K0sWorkerConfigTemplate
    metadata:
      name: docker-test-1 # New name
      namespace: default
    spec:
      template:
        spec:
          args:
          - --enable-cloud-provider
          - --kubelet-extra-args="--cloud-provider=external"
          version: v1.28.7+k0s.0 # new k0s version
   ```
5. Edit the MachineDeployment with the new version of the K0sWorkerConfigTemplate start the rollout:
 ```yaml
    ---
    apiVersion: cluster.x-k8s.io/v1beta1
    kind: MachineDeployment
    {.....}
    spec:
      bootstrap:
        configRef:
          apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
          kind: K0sWorkerConfigTemplate
          name: docker-test-1
          namespace: default
```
The update procedure is completed, you now have the target version of k0smotron.
