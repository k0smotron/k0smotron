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
	"fmt"
	"time"

	"github.com/k0sproject/k0smotron/internal/controller/util"

	km "github.com/k0sproject/k0smotron/api/k0smotron.io/v1beta1"
	kcontrollerutil "github.com/k0sproject/k0smotron/internal/controller/util"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func generateService(kmc *km.Cluster) v1.Service {
	var name string
	ports := []v1.ServicePort{}
	switch kmc.Spec.Service.Type {
	case v1.ServiceTypeNodePort:
		name = kmc.GetNodePortServiceName()
		ports = append(ports,
			v1.ServicePort{
				Port:       int32(kmc.Spec.Service.APIPort),
				TargetPort: intstr.FromInt(kmc.Spec.Service.APIPort),
				Name:       "api",
				NodePort:   int32(kmc.Spec.Service.APIPort),
			},
			v1.ServicePort{
				Port:       int32(kmc.Spec.Service.KonnectivityPort),
				TargetPort: intstr.FromInt(kmc.Spec.Service.KonnectivityPort),
				Name:       "konnectivity",
				NodePort:   int32(kmc.Spec.Service.KonnectivityPort),
			})
	case v1.ServiceTypeLoadBalancer:
		name = kmc.GetLoadBalancerServiceName()
		// LB svc does not define the nodeport so it can be dynamically assigned
		ports = append(ports,
			v1.ServicePort{
				Port:       int32(kmc.Spec.Service.APIPort),
				TargetPort: intstr.FromInt(kmc.Spec.Service.APIPort),
				Name:       "api",
			},
			v1.ServicePort{
				Port:       int32(kmc.Spec.Service.KonnectivityPort),
				TargetPort: intstr.FromInt(kmc.Spec.Service.KonnectivityPort),
				Name:       "konnectivity",
			})
	case v1.ServiceTypeClusterIP:
		// ClusterIP is the default
		fallthrough
	default:
		// Default to ClusterIP
		kmc.Spec.Service.Type = v1.ServiceTypeClusterIP
		name = kmc.GetClusterIPServiceName()

		ports = append(ports,
			v1.ServicePort{
				Port:       int32(kmc.Spec.Service.APIPort),
				TargetPort: intstr.FromInt(kmc.Spec.Service.APIPort),
				Name:       "api",
			},
			v1.ServicePort{
				Port:       int32(kmc.Spec.Service.KonnectivityPort),
				TargetPort: intstr.FromInt(kmc.Spec.Service.KonnectivityPort),
				Name:       "konnectivity",
			})
	}

	// Copy both Cluster level labels and Service labels
	labels := map[string]string{}
	for k, v := range kcontrollerutil.LabelsForK0smotronCluster(kmc) {
		labels[k] = v
	}
	for k, v := range kmc.Spec.Service.Labels {
		labels[k] = v
	}

	// Copy both Cluster level annotations and Service annotations
	annotations := map[string]string{}
	for k, v := range kcontrollerutil.AnnotationsForK0smotronCluster(kmc) {
		annotations[k] = v
	}
	for k, v := range kmc.Spec.Service.Annotations {
		annotations[k] = v
	}

	svc := v1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   kmc.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: v1.ServiceSpec{
			Type:     kmc.Spec.Service.Type,
			Selector: labels,
			Ports:    ports,
		},
	}

	if kmc.Spec.Service.Type == v1.ServiceTypeLoadBalancer {
		svc.Spec.LoadBalancerClass = kmc.Spec.Service.LoadBalancerClass
	}

	return svc
}

func (scope *kmcScope) reconcileServices(ctx context.Context, kmc *km.Cluster) error {
	logger := log.FromContext(ctx)
	// Depending on ingress configuration create nodePort service.
	logger.Info("Reconciling services")
	svc := generateService(kmc)

	_ = ctrl.SetControllerReference(kmc, &svc, scope.client.Scheme())

	if err := scope.client.Patch(ctx, &svc, client.Apply, patchOpts...); err != nil {
		return err
	}
	// Wait for LB address to be available
	logger.Info("Waiting for loadbalancer address")
	if kmc.Spec.Service.Type == v1.ServiceTypeLoadBalancer && kmc.Spec.ExternalAddress == "" {
		err := wait.PollUntilContextTimeout(ctx, 1*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
			err := scope.client.Get(ctx, client.ObjectKey{Name: svc.Name, Namespace: svc.Namespace}, &svc)
			if err != nil {
				return false, err
			}
			if len(svc.Status.LoadBalancer.Ingress) > 0 {
				if svc.Status.LoadBalancer.Ingress[0].Hostname != "" {
					kmc.Spec.ExternalAddress = svc.Status.LoadBalancer.Ingress[0].Hostname
				}
				if svc.Status.LoadBalancer.Ingress[0].IP != "" {
					kmc.Spec.ExternalAddress = svc.Status.LoadBalancer.Ingress[0].IP
				}
				logger.Info("Loadbalancer address available, updating Cluster object", "address", kmc.Spec.ExternalAddress)

				err := scope.client.Update(ctx, kmc)
				if err != nil {
					return false, err
				}
				return true, nil
			}
			return false, nil
		})
		if err != nil {
			return fmt.Errorf("failed to get loadbalancer address: %w", err)
		}
	} else if kmc.Spec.Service.Type == v1.ServiceTypeNodePort && kmc.Spec.ExternalAddress == "" {
		logger.Info("finding nodeport address")
		// Get a random node address as external address
		nodes := &v1.NodeList{}
		err := scope.client.List(ctx, nodes)
		if err != nil {
			return err
		}
		kmc.Spec.ExternalAddress = util.FindNodeAddress(nodes)
	}

	return nil
}
