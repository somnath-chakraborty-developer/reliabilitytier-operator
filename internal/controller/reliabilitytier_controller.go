/*
Copyright 2026.

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

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	platformv1 "github.com/somnath/reliabilitytier-operator/api/v1"
)

const reliabilityTierFinalizer = "platform.platform.example.com/finalizer"

// ReliabilityTierReconciler reconciles a ReliabilityTier object
type ReliabilityTierReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=platform.platform.example.com,resources=reliabilitytiers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.platform.example.com,resources=reliabilitytiers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.platform.example.com,resources=reliabilitytiers/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=resourcequotas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete

// tierSpec holds the ResourceQuota and PodDisruptionBudget numbers for one tier.
type tierSpec struct {
	cpuRequest   string
	memRequest   string
	cpuLimit     string
	memLimit     string
	minAvailable string
}

// tierSpecs is the lookup table: Topic 5's "desired state" translated into
// concrete numbers per tier. Bronze gets the least protection, Gold the most.
var tierSpecs = map[string]tierSpec{
	"Bronze": {cpuRequest: "2", memRequest: "4Gi", cpuLimit: "4", memLimit: "8Gi", minAvailable: "0"},
	"Silver": {cpuRequest: "8", memRequest: "16Gi", cpuLimit: "16", memLimit: "32Gi", minAvailable: "1"},
	"Gold":   {cpuRequest: "32", memRequest: "64Gi", cpuLimit: "64", memLimit: "128Gi", minAvailable: "50%"},
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ReliabilityTierReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch fresh — req is only a namespace/name key (Topic 7).
	var rt platformv1.ReliabilityTier
	if err := r.Get(ctx, req.NamespacedName, &rt); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Finalizer handling: delete-that-isn't-really-a-delete (Topic 7).
	if rt.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(&rt, reliabilityTierFinalizer) {
			controllerutil.AddFinalizer(&rt, reliabilityTierFinalizer)
			if err := r.Update(ctx, &rt); err != nil {
				return ctrl.Result{}, err
			}
		}
	} else {
		// OwnerReferences on the child objects mean Kubernetes garbage-collects
		// them automatically (Topic 5) once this object is actually removed —
		// so cleanup here is just releasing our finalizer.
		if controllerutil.ContainsFinalizer(&rt, reliabilityTierFinalizer) {
			controllerutil.RemoveFinalizer(&rt, reliabilityTierFinalizer)
			if err := r.Update(ctx, &rt); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	spec, ok := tierSpecs[rt.Spec.Tier]
	if !ok {
		log.Error(nil, "unknown tier", "tier", rt.Spec.Tier)
		return ctrl.Result{}, fmt.Errorf("unknown tier %q", rt.Spec.Tier)
	}

	// ResourceQuota, sized by tier.
	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rt.Name + "-quota",
			Namespace: rt.Namespace,
		},
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, quota, func() error {
		quota.Spec.Hard = corev1.ResourceList{
			corev1.ResourceRequestsCPU:    resource.MustParse(spec.cpuRequest),
			corev1.ResourceRequestsMemory: resource.MustParse(spec.memRequest),
			corev1.ResourceLimitsCPU:      resource.MustParse(spec.cpuLimit),
			corev1.ResourceLimitsMemory:   resource.MustParse(spec.memLimit),
		}
		return controllerutil.SetControllerReference(&rt, quota, r.Scheme)
	}); err != nil {
		return ctrl.Result{}, err
	}

	// PodDisruptionBudget, sized by tier.
	minAvail := intstr.Parse(spec.minAvailable)
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rt.Name + "-pdb",
			Namespace: rt.Namespace,
		},
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, pdb, func() error {
		pdb.Spec.MinAvailable = &minAvail
		pdb.Spec.Selector = &metav1.LabelSelector{} // matches all pods in namespace
		return controllerutil.SetControllerReference(&rt, pdb, r.Scheme)
	}); err != nil {
		return ctrl.Result{}, err
	}

	// Status: the "actual state" half of the reconcile loop (Topic 5/7).
	meta.SetStatusCondition(&rt.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionTrue,
		Reason:  "TierEnforced",
		Message: fmt.Sprintf("ResourceQuota and PodDisruptionBudget enforced for tier %s", rt.Spec.Tier),
	})
	if err := r.Status().Update(ctx, &rt); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ReliabilityTierReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1.ReliabilityTier{}).
		Owns(&corev1.ResourceQuota{}).
		Owns(&policyv1.PodDisruptionBudget{}).
		Named("reliabilitytier").
		Complete(r)
}
