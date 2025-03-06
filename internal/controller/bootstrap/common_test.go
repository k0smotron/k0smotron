package bootstrap

import (
	"fmt"
	"testing"

	"github.com/k0smotron/k0smotron/api/bootstrap/v1beta1"
	"github.com/k0smotron/k0smotron/internal/cloudinit"
	"github.com/k0smotron/k0smotron/internal/util"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestResolveFiles(t *testing.T) {
	ns, err := testEnv.CreateNamespace(ctx, "test-resolve-files")
	require.NoError(t, err)

	cluster := newCluster(ns.Name)
	require.NoError(t, testEnv.Create(ctx, cluster))

	secretRef := "my-secret-file-content"
	secretKeyRef := "key"
	secretContent := "secretcontent"
	secretFileContent := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretRef,
			Namespace: ns.Name,
		},
		Data: map[string][]byte{
			secretKeyRef: []byte(secretContent),
		},
	}
	require.NoError(t, testEnv.Create(ctx, secretFileContent))

	configmapRef := "my-configmap-file-content"
	configmapKeyRef := "key"
	configmapContent := "configmapcontent"
	configmapFileContent := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configmapRef,
			Namespace: ns.Name,
		},
		Data: map[string]string{
			configmapKeyRef: configmapContent,
		},
	}
	require.NoError(t, testEnv.Create(ctx, configmapFileContent))

	defer func(do ...client.Object) {
		require.NoError(t, testEnv.Cleanup(ctx, do...))
	}(cluster, ns, secretFileContent, configmapFileContent)

	filesToResolve := []v1beta1.File{
		{
			ContentFrom: &v1beta1.ContentSource{
				SecretRef: &v1beta1.ContentSourceRef{
					Name: secretRef,
					Key:  secretKeyRef,
				},
			},
		},
		{
			ContentFrom: &v1beta1.ContentSource{
				ConfigMapRef: &v1beta1.ContentSourceRef{
					Name: configmapRef,
					Key:  configmapKeyRef,
				},
			},
		},
	}
	expectedOutput := []cloudinit.File{
		{
			Content: secretContent,
		},
		{
			Content: configmapContent,
		},
	}
	output, err := resolveFiles(ctx, testEnv, cluster, filesToResolve)
	require.NoError(t, err, errExtractingFileContent)
	require.Equal(t, expectedOutput, output)
}

func TestResolveFilesErrorExtractingFileContent(t *testing.T) {
	ns, err := testEnv.CreateNamespace(ctx, "test-resolve-files-invalid-content-location")
	require.NoError(t, err)

	cluster := newCluster(ns.Name)
	require.NoError(t, testEnv.Create(ctx, cluster))

	defer func(do ...client.Object) {
		require.NoError(t, testEnv.Cleanup(ctx, do...))
	}(cluster, ns)

	// source references to non existing resources

	filesToResolve := []v1beta1.File{
		{
			ContentFrom: &v1beta1.ContentSource{
				SecretRef: &v1beta1.ContentSourceRef{
					Name: "test",
					Key:  "test",
				},
			},
		},
	}
	_, err = resolveFiles(ctx, testEnv, cluster, filesToResolve)
	require.ErrorIs(t, err, errExtractingFileContent)

	filesToResolve = []v1beta1.File{
		{
			ContentFrom: &v1beta1.ContentSource{
				ConfigMapRef: &v1beta1.ContentSourceRef{
					Name: "test",
					Key:  "test",
				},
			},
		},
	}
	_, err = resolveFiles(ctx, testEnv, cluster, filesToResolve)
	require.ErrorIs(t, err, errExtractingFileContent)

	// source references to non existing resource key

	secretRef := "my-secret-file-content"
	secretFileContent := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretRef,
			Namespace: ns.Name,
		},
		Data: map[string][]byte{
			"realkey": []byte("somecontent"),
		},
	}
	err = testEnv.Create(ctx, secretFileContent)
	require.NoError(t, err)
	filesToResolve = []v1beta1.File{
		{
			ContentFrom: &v1beta1.ContentSource{
				SecretRef: &v1beta1.ContentSourceRef{
					Name: secretRef,
					Key:  "nonexistingkey",
				},
			},
		},
	}
	_, err = resolveFiles(ctx, testEnv, cluster, filesToResolve)
	require.ErrorIs(t, err, errExtractingFileContent)

	configmapRef := "my-configmap-file-content"
	configmapFileContent := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configmapRef,
			Namespace: ns.Name,
		},
		Data: map[string]string{
			"realkey": "somecontent",
		},
	}
	err = testEnv.Create(ctx, configmapFileContent)
	require.NoError(t, err)
	filesToResolve = []v1beta1.File{
		{
			ContentFrom: &v1beta1.ContentSource{
				ConfigMapRef: &v1beta1.ContentSourceRef{
					Name: configmapRef,
					Key:  "nonexistingkey",
				},
			},
		},
	}
	_, err = resolveFiles(ctx, testEnv, cluster, filesToResolve)
	require.ErrorIs(t, err, errExtractingFileContent)

	filesToResolve = []v1beta1.File{
		{
			ContentFrom: &v1beta1.ContentSource{},
		},
	}
	_, err = resolveFiles(ctx, testEnv, cluster, filesToResolve)
	require.ErrorIs(t, err, errExtractingFileContent)
}

func newCluster(namespace string) *clusterv1.Cluster {
	clusterName := fmt.Sprintf("foo-%s", util.RandomString(6))

	return &clusterv1.Cluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Cluster",
			APIVersion: clusterv1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      clusterName,
		},
		Spec: clusterv1.ClusterSpec{
			ControlPlaneEndpoint: clusterv1.APIEndpoint{
				Host: "test.host",
				Port: 9999,
			},
		},
	}
}
