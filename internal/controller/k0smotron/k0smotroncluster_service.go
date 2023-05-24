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

package k0smotron

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	km "github.com/k0sproject/k0smotron/api/k0smotron/v1beta1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	informerv1 "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func (r *ClusterReconciler) generateService(kmc *km.Cluster) v1.Service {
	var name string
	ports := []v1.ServicePort{}
	switch kmc.Spec.Service.Type {
	case v1.ServiceTypeNodePort:
		name = kmc.GetNodePortName()
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
		name = kmc.GetLoadBalancerName()
		ports = append(ports,
			v1.ServicePort{
				Port:       int32(6443),
				TargetPort: intstr.FromInt(6443),
				Name:       "api",
			},
			v1.ServicePort{
				Port:       int32(8132),
				TargetPort: intstr.FromInt(8132),
				Name:       "konnectivity",
			})
	}

	svc := v1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   kmc.Namespace,
			Labels:      map[string]string{"app": "k0smotron"},
			Annotations: kmc.Spec.Service.Annotations,
		},
		Spec: v1.ServiceSpec{
			Type:     kmc.Spec.Service.Type,
			Selector: map[string]string{"app": "k0smotron"},
			Ports:    ports,
		},
	}

	_ = ctrl.SetControllerReference(kmc, &svc, r.Scheme)

	return svc
}

func (r *ClusterReconciler) reconcileServices(ctx context.Context, kmc km.Cluster) error {
	logger := log.FromContext(ctx)
	// Depending on ingress configuration create nodePort service.
	logger.Info("Reconciling services")
	svc := r.generateService(&kmc)
	// if kmc.Spec.Service.Type == v1.ServiceTypeLoadBalancer && kmc.Spec.ExternalAddress == "" {
	// 	err := r.watchLBServiceIP(ctx, kmc)
	// 	if err != nil {
	// 		return err
	// 	}
	// }
	if err := r.Client.Patch(ctx, &svc, client.Apply, patchOpts...); err != nil {
		return err
	}
	// Wait for LB address to be available
	logger.Info("Waiting for loadbalancer address")
	if kmc.Spec.Service.Type == v1.ServiceTypeLoadBalancer && kmc.Spec.ExternalAddress == "" {
		err := wait.PollImmediateWithContext(ctx, 1*time.Second, 2*time.Minute, func(ctx context.Context) (bool, error) {
			err := r.Client.Get(ctx, client.ObjectKey{Name: svc.Name, Namespace: svc.Namespace}, &svc)
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

				err := r.Client.Update(ctx, &kmc)
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
		// Get a random node address as external address
		nodes, err := r.ClientSet.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}
		if len(nodes.Items) == 0 {
			return fmt.Errorf("no nodes found")
		}
		// Get random node from list
		node := nodes.Items[rand.Intn(len(nodes.Items))]
		for _, addr := range node.Status.Addresses {
			if addr.Type == v1.NodeExternalIP {
				kmc.Spec.ExternalAddress = addr.Address
				break
			}
			if addr.Type == v1.NodeInternalIP {
				kmc.Spec.ExternalAddress = addr.Address
				break
			}
		}
	}

	return nil
}

func (r *ClusterReconciler) watchLBServiceIP(ctx context.Context, kmc km.Cluster) error {
	var handler cache.ResourceEventHandlerFuncs
	stopCh := make(chan struct{})
	handler.UpdateFunc = func(old, new interface{}) {
		newObj, ok := new.(*v1.Service)
		if !ok {
			return
		}

		var externalAddress string
		if len(newObj.Status.LoadBalancer.Ingress) > 0 {
			if newObj.Status.LoadBalancer.Ingress[0].Hostname != "" {
				externalAddress = newObj.Status.LoadBalancer.Ingress[0].Hostname
			}
			if newObj.Status.LoadBalancer.Ingress[0].IP != "" {
				externalAddress = newObj.Status.LoadBalancer.Ingress[0].IP
			}

			if externalAddress != "" {
				kmc.Spec.ExternalAddress = externalAddress
				err := r.Client.Update(ctx, &kmc)
				if err != nil {
					log.FromContext(ctx).Error(err, "failed to update K0smotronCluster")
				}
				stopCh <- struct{}{}
			}
		}
	}

	inf := informerv1.NewServiceInformer(r.ClientSet, kmc.Namespace, 100*time.Millisecond, nil)
	_, err := inf.AddEventHandler(handler)
	if err != nil {
		return err
	}

	go inf.Run(stopCh)

	return nil
}
