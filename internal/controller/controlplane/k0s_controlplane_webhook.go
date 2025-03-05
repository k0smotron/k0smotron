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

package controlplane

import (
	"context"
	"fmt"

	"github.com/k0sproject/version"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/k0smotron/k0smotron/api/controlplane/v1beta1"
)

// +kubebuilder:webhook:path=/validate-controlplane-cluster-x-k8s-io-v1beta1-k0scontrolplane,mutating=false,failurePolicy=fail,sideEffects=None,groups=controlplane.cluster.x-k8s.io,resources=k0scontrolplanes,verbs=create;update,versions=v1beta1,name=validate-k0scontrolplane-v1beta1.k0smotron.io,admissionReviewVersions=v1

// K0sControlPlaneValidator struct is responsible for validating the K0sControlPlane resource when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type K0sControlPlaneValidator struct {
	//TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &K0sControlPlaneValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type K0sControlPlane.
func (v *K0sControlPlaneValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	kcp, ok := obj.(*v1beta1.K0sControlPlane)
	if !ok {
		return nil, fmt.Errorf("expected a K0sControlPlane object but got %T", obj)
	}

	return nil, validateK0sControlPlane(kcp)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type K0sControlPlane.
func (v *K0sControlPlaneValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	newKCP, ok := newObj.(*v1beta1.K0sControlPlane)
	if !ok {
		return nil, fmt.Errorf("expected a new K0sControlPlane object but got %T", newObj)
	}
	oldKCP, ok := oldObj.(*v1beta1.K0sControlPlane)
	if !ok {
		return nil, fmt.Errorf("expected a old K0sControlPlane object but got %T", oldObj)
	}

	if oldKCP.Spec.Version != newKCP.Spec.Version {
		oldV, err := version.NewVersion(oldKCP.Spec.Version)
		if err != nil {
			return nil, fmt.Errorf("failed to parse old version: %v", err)
		}
		newV, err := version.NewVersion(newKCP.Spec.Version)
		if err != nil {
			return nil, fmt.Errorf("failed to parse new version: %v", err)
		}

		// According to the Kubernetes skew policy, we can't upgrade more than one minor version at a time.
		if newV.Core().Segments()[1]-oldV.Core().Segments()[1] > 1 {
			return nil, fmt.Errorf("upgrading more than one minor version at a time is not allowed by the Kubernetes skew policy")
		}
	}

	return nil, validateK0sControlPlane(newKCP)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type K0sControlPlane.
func (v *K0sControlPlaneValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func validateK0sControlPlane(kcp *v1beta1.K0sControlPlane) error {
	if err := denyUncompatibleK0sVersions(kcp); err != nil {
		return err
	}

	// nolint:revive
	if err := denyRecreateOnSingleClusters(kcp); err != nil {
		return err
	}

	return nil
}

func denyUncompatibleK0sVersions(kcp *v1beta1.K0sControlPlane) error {
	var uncomaptibleVersions = map[string]string{
		"1.31.1": "v1.31.2+",
	}
	v, err := version.NewVersion(kcp.Spec.Version)
	if err != nil {
		return fmt.Errorf("failed to parse version: %v", err)
	}

	if vv, ok := uncomaptibleVersions[v.Core().String()]; ok {
		return fmt.Errorf("version %s is not compatible with K0sControlPlane, use %s", kcp.Spec.Version, vv)
	}

	return nil
}

func denyRecreateOnSingleClusters(kcp *v1beta1.K0sControlPlane) error {
	if kcp.Spec.UpdateStrategy == v1beta1.UpdateRecreate {

		// If the cluster is running in single mode, we can't use the Recreate strategy
		if kcp.Spec.K0sConfigSpec.Args != nil {
			for _, arg := range kcp.Spec.K0sConfigSpec.Args {
				if arg == "--single" {
					return fmt.Errorf("UpdateStrategy Recreate strategy is not allowed when the cluster is running in single mode")
				}
			}
		}
	}

	return nil
}

// SetupK0sControlPlaneWebhookWithManager registers the webhook for K0sControlPlane in the manager.
func (v *K0sControlPlaneValidator) SetupK0sControlPlaneWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&v1beta1.K0sControlPlane{}).
		WithValidator(v).
		Complete()
}
