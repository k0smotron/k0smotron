# k0smotron compatibility matrix

k0smotron functions as a Cluster API compliant infrastructure provider for k0s
that enables cluster provisioning on remote machines using SSH connections.

Commercial support for k0smotron if offered by [Mirantis Inc](https://www.mirantis.com/).

## Kubernetes and k0s

| k0smotron | Kubernetes version | k0s version    |
|-----------|--------------------|----------------|
| TBD       | 1.22 and above     | 1.22 and above |

The compatibility of k0smotron is determined by the compatibility of the k0s and Kubernetes versions utilized in your project.

##  Cluster API

k0smotron can work with any Cluster API tooling compatible with your version of Kubernetes.
Refer to official Cluster API documentation
[Cluster API Version Support and Kubernetes Version Skew Policy](https://cluster-api.sigs.k8s.io/reference/versions#supported-kubernetes-versions).
