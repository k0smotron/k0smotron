# Using Horizontal Pod Autoscaler (HPA) for k0smotron.io/Cluster

## Introduction

Horizontal Pod Autoscaler (HPA) automatically scales the number of pods in a Kubernetes cluster based on observed CPU utilization (or other select metrics). This guide will walk you through the steps to set up and use HPA for `k0smotron.io/Cluster`.

!!! warning

    Due to etcd maintanance challenges, k0smotron **never** scales etcd statefulsets down, only up. 
    This means that HPA will scale up both control-plane and etcd, but scale down only control-plane pods.

## Prerequisites

- A running Kubernetes cluster
- `kubectl` command-line tool installed and configured
- Metrics Server installed in your cluster

## Step-by-Step Guide

### 1. Install Metrics Server

Metrics Server is a cluster-wide aggregator of resource usage data. It is required for HPA to function.
[k0s](https://k0sproject.io/) brings metrics-server by default, but you can install it manually if needed.

```sh
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
```

### 2. Define Resource Requests and Limits

Ensure your k0smotron.io/Cluster has CPU and/or memory requests and limits defined. HPA uses these values to make scaling decisions.  Example k0smotron.io/Cluster definition:

```yaml
apiVersion: k0smotron.io/v1beta1
kind: Cluster
metadata:
  name: example-cluster
  namespace: default
spec:
  replicas: 1
  version: "v1.31.5-k0s.0"
  service:
    type: NodePort
  resources:
    requests:
      cpu: "100m"
      memory: "100Mi"
```

### 3. Create an HPA

Create an HPA resource to automatically scale your k0smotron.io/Cluster based on CPU utilization.

```yaml
apiVersion: autoscaling/v2beta2
kind: HorizontalPodAutoscaler
metadata:
  name: example-hpa
  namespace: default
spec:
  scaleTargetRef:
    apiVersion: k0smotron.io/v1beta1
    kind: Cluster
    name: example-cluster
  minReplicas: 1
  maxReplicas: 10
  metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 50
```

Apply the HPA:

```sh
kubectl get hpa -n default
```

### 4. Verify HPA

Check the status of your HPA to ensure it is working correctly.

```sh
kubectl get hpa -n default
```

You should see output similar to:

```sh
NAME         REFERENCE               TARGETS   MINPODS   MAXPODS   REPLICAS   AGE
example-hpa  Cluster/example-cluster  10%/50%    1         10        1          1m
```

You have successfully set up Horizontal Pod Autoscaler (HPA) for your k0smotron.io/Cluster. HPA will now automatically adjust the number of pods in your cluster based on the specified metrics.  For more information, refer to the [Kubernetes HPA documentation](https://kubernetes.io/docs/tasks/run-application/horizontal-pod-autoscale/).