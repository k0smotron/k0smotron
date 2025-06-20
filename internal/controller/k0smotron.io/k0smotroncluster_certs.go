/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package k0smotronio

import (
	"context"
	"crypto/x509"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	bootstrapv1 "sigs.k8s.io/cluster-api/bootstrap/kubeadm/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/certs"

	"github.com/cloudflare/cfssl/cli/genkey"
	"github.com/cloudflare/cfssl/csr"
	"github.com/cloudflare/cfssl/helpers"
	"github.com/cloudflare/cfssl/signer"
	"github.com/cloudflare/cfssl/signer/local"
	"sigs.k8s.io/cluster-api/util/secret"

	km "github.com/k0sproject/k0smotron/api/k0smotron.io/v1beta1"
)

func (scope *kmcScope) ensureEtcdCertificates(ctx context.Context, kmc *km.Cluster) error {
	certificates := secret.NewCertificatesForInitialControlPlane(&bootstrapv1.ClusterConfiguration{})
	err := certificates.LookupCached(ctx, scope.secretCachingClient, scope.client, util.ObjectKey(kmc))
	if err != nil {
		return fmt.Errorf("error looking up etcd certs: %w", err)
	}
	etcdCACert := certificates.GetByPurpose(secret.EtcdCA)
	if etcdCACert.KeyPair == nil || len(etcdCACert.KeyPair.Cert) == 0 {
		return fmt.Errorf("etcd CA certificate not found")
	}

	caCert, err := helpers.ParseCertificatePEM(etcdCACert.KeyPair.Cert)
	if err != nil {
		return fmt.Errorf("error parsing etcd CA certificate: %w", err)
	}

	caPrivKey, err := helpers.ParsePrivateKeyPEM(etcdCACert.KeyPair.Key)
	if err != nil {
		return fmt.Errorf("error parsing etcd CA private key: %w", err)
	}

	signr, err := local.NewSigner(caPrivKey, caCert, x509.SHA256WithRSA, nil)
	if err != nil {
		return fmt.Errorf("error creating signer: %w", err)
	}

	g := &csr.Generator{Validator: genkey.Validator}

	etcdCerts := secret.Certificates{
		&secret.Certificate{Purpose: "apiserver-etcd-client"},
		&secret.Certificate{Purpose: "etcd-server"},
		&secret.Certificate{Purpose: "etcd-peer"},
	}

	err = etcdCerts.LookupCached(ctx, scope.secretCachingClient, scope.client, util.ObjectKey(kmc))
	if err != nil {
		return fmt.Errorf("error looking up etcd certs: %w", err)
	}

	for _, c := range etcdCerts {
		if c.KeyPair == nil {
			req := csr.CertificateRequest{
				KeyRequest: csr.NewKeyRequest(),
				CN:         string(c.Purpose),
				Names: []csr.Name{
					{O: string(c.Purpose)},
				},
				Hosts: []string{
					"127.0.0.1",
					"localhost",
					kmc.GetEtcdServiceName(),
					fmt.Sprintf("%s.%s.svc", kmc.GetEtcdServiceName(), kmc.GetNamespace()),
					fmt.Sprintf("%s.%s.svc.cluster.local", kmc.GetEtcdServiceName(), kmc.GetNamespace()),
					fmt.Sprintf("*.%s", kmc.GetEtcdServiceName()),
					fmt.Sprintf("*.%s.%s.svc", kmc.GetEtcdServiceName(), kmc.GetNamespace()),
					fmt.Sprintf("*.%s.%s.svc.cluster.local", kmc.GetEtcdServiceName(), kmc.GetNamespace()),
				},
			}
			req.KeyRequest.A = "rsa"
			req.KeyRequest.S = 2048

			csrBytes, key, err := g.ProcessRequest(&req)
			if err != nil {
				return fmt.Errorf("error processing csr: %w", err)
			}
			cert, err := signr.Sign(signer.SignRequest{
				Request: string(csrBytes),
				Profile: "kubernetes",
			})
			if err != nil {
				return fmt.Errorf("error signing csr: %w", err)
			}

			c.Generated = true
			c.KeyPair = &certs.KeyPair{Cert: cert, Key: key}
		}
	}

	return etcdCerts.SaveGenerated(ctx, scope.client, util.ObjectKey(kmc), *metav1.NewControllerRef(kmc, km.GroupVersion.WithKind("Cluster")))
}
