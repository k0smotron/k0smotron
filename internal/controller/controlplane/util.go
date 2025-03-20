package controlplane

import (
	"context"
	"fmt"

	"github.com/imdario/mergo"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/certs"
	"sigs.k8s.io/cluster-api/util/kubeconfig"
	"sigs.k8s.io/cluster-api/util/labels/format"
	"sigs.k8s.io/cluster-api/util/secret"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cpv1beta1 "github.com/k0smotron/k0smotron/api/controlplane/v1beta1"
	k0smoutil "github.com/k0smotron/k0smotron/internal/controller/util"
)

func (c *K0sController) getMachineTemplate(ctx context.Context, kcp *cpv1beta1.K0sControlPlane) (*unstructured.Unstructured, error) {
	infRef := kcp.Spec.MachineTemplate.InfrastructureRef

	infraMachineTemplate := new(unstructured.Unstructured)
	infraMachineTemplate.SetAPIVersion(infRef.APIVersion)
	infraMachineTemplate.SetKind(infRef.Kind)
	infraMachineTemplate.SetName(infRef.Name)

	key := client.ObjectKey{Name: infRef.Name, Namespace: infRef.Namespace}

	err := c.Get(ctx, key, infraMachineTemplate)
	if err != nil {
		return nil, err
	}
	return infraMachineTemplate, nil
}

func (c *K0sController) generateKubeconfig(ctx context.Context, clusterKey client.ObjectKey, endpoint string) (*api.Config, error) {
	clusterCA, err := secret.GetFromNamespacedName(ctx, c.SecretCachingClient, clusterKey, secret.ClusterCA)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, kubeconfig.ErrDependentCertificateNotFound
		}
		return nil, err
	}

	cert, err := certs.DecodeCertPEM(clusterCA.Data[secret.TLSCrtDataName])
	if err != nil {
		return nil, fmt.Errorf("failed to decode CA Cert: %w", err)
	} else if cert == nil {
		return nil, fmt.Errorf("certificate not found in config: %w", err)
	}

	key, err := certs.DecodePrivateKeyPEM(clusterCA.Data[secret.TLSKeyDataName])
	if err != nil {
		return nil, fmt.Errorf("failed to decode private key: %w", err)
	} else if key == nil {
		return nil, fmt.Errorf("CA private key not found: %w", err)
	}

	cfg, err := kubeconfig.New(clusterKey.Name, endpoint, cert, key)
	if err != nil {
		return nil, fmt.Errorf("failed to generate a kubeconfig: %w", err)
	}

	return cfg, nil

}

func (c *K0sController) createKubeconfigSecret(ctx context.Context, cfg *api.Config, cluster *clusterv1.Cluster, secretName string) error {
	cfgBytes, err := clientcmd.Write(*cfg)
	if err != nil {
		return fmt.Errorf("failed to serialize config to yaml: %w", err)
	}

	clusterName := util.ObjectKey(cluster)
	owner := metav1.OwnerReference{
		APIVersion: clusterv1.GroupVersion.String(),
		Kind:       "Cluster",
		Name:       cluster.Name,
		UID:        cluster.UID,
	}

	kcSecret := kubeconfig.GenerateSecretWithOwner(clusterName, cfgBytes, owner)
	kcSecret.Name = secretName

	return c.Create(ctx, kcSecret)
}

func (c *K0sController) regenerateKubeconfigSecret(ctx context.Context, kubeconfigSecret *v1.Secret, clusterName string) error {
	data, ok := kubeconfigSecret.Data[secret.KubeconfigDataName]
	if !ok {
		return fmt.Errorf("missing key %q in secret data", secret.KubeconfigDataName)
	}

	oldConfig, err := clientcmd.Load(data)
	if err != nil {
		return fmt.Errorf("failed to convert kubeconfig Secret into a clientcmdapi.Config: %w", err)
	}

	endpoint := oldConfig.Clusters[clusterName].Server

	clusterKey := client.ObjectKey{
		Name: clusterName,
		// The namespace of the current kubeconfig secret can be used, as it is always
		// created in the cluster's namespace.
		Namespace: kubeconfigSecret.Namespace,
	}
	newConfig, err := c.generateKubeconfig(ctx, clusterKey, endpoint)
	if err != nil {
		return err
	}

	// The proxy URL needs to be set for the new secret using the old value. That way we
	// cover cases when tunneling mode = "proxy" and proxy url exists.
	for cn := range newConfig.Clusters {
		newConfig.Clusters[cn].ProxyURL = oldConfig.Clusters[clusterName].ProxyURL
	}

	out, err := clientcmd.Write(*newConfig)
	if err != nil {
		return fmt.Errorf("failed to serialize config to yaml: %w", err)
	}
	kubeconfigSecret.Data[secret.KubeconfigDataName] = out

	return c.Update(ctx, kubeconfigSecret)
}

func (c *K0sController) getKubeClient(ctx context.Context, cluster *clusterv1.Cluster) (*kubernetes.Clientset, error) {
	if c.workloadClusterKubeClient != nil {
		return c.workloadClusterKubeClient, nil
	}

	return k0smoutil.GetKubeClient(ctx, c.SecretCachingClient, cluster)
}

func enrichK0sConfigWithClusterData(cluster *clusterv1.Cluster, k0sConfig *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if cluster.Spec.ClusterNetwork == nil {
		return k0sConfig, nil
	}

	clusterNetworkValues := make(map[string]interface{})
	if cluster.Spec.ClusterNetwork.Pods != nil {
		clusterNetworkValues["podCIDR"] = cluster.Spec.ClusterNetwork.Pods.String()
	}
	if cluster.Spec.ClusterNetwork.Services != nil {
		clusterNetworkValues["serviceCIDR"] = cluster.Spec.ClusterNetwork.Services.String()
	}
	if cluster.Spec.ClusterNetwork.ServiceDomain != "" {
		clusterNetworkValues["clusterDomain"] = cluster.Spec.ClusterNetwork.ServiceDomain
	}

	if k0sConfig == nil {
		k0sConfig = &unstructured.Unstructured{}
	}

	clusterValues := map[string]interface{}{
		"apiVersion": "k0s.k0sproject.io/v1beta1",
		"kind":       "ClusterConfig",
		"spec": map[string]interface{}{
			"network": clusterNetworkValues,
		},
	}

	err := mergo.Merge(&k0sConfig.Object, clusterValues)
	return k0sConfig, err
}

func controlPlaneCommonLabelsForCluster(kcp *cpv1beta1.K0sControlPlane, clusterName string) map[string]string {
	labels := map[string]string{}

	// Add the labels from the MachineTemplate.
	// Note: we intentionally don't use the map directly to ensure we don't modify the map in KCP.
	for k, v := range kcp.Spec.MachineTemplate.ObjectMeta.Labels {
		labels[k] = v
	}

	// Always force these labels over the ones coming from the spec.
	labels[clusterv1.ClusterNameLabel] = clusterName
	labels[clusterv1.MachineControlPlaneLabel] = "true"
	// Note: MustFormatValue is used here as the label value can be a hash if the control plane name is longer than 63 characters.
	labels[clusterv1.MachineControlPlaneNameLabel] = format.MustFormatValue(kcp.Name)
	return labels
}
