/*
Copyright 2022 The OpenFunction Authors.

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

package networking

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/json"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	k8sgatewayapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	networkingv1alpha1 "github.com/openfunction/apis/networking/v1alpha1"
	"github.com/openfunction/pkg/util"
)

const (
	istioGatewayClassName   = "istio"
	contourGatewayClassName = "contour"
)

// GatewayReconciler reconciles a Gateway object
type GatewayReconciler struct {
	client.Client
	Log        logr.Logger
	Scheme     *runtime.Scheme
	ctx        context.Context
	k8sGateway *k8sgatewayapiv1alpha2.Gateway
}

//+kubebuilder:rbac:groups=networking.openfunction.io,resources=gateways,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.openfunction.io,resources=gateways/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=networking.openfunction.io,resources=gateways/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Gateway object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (r *GatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	//_ = log.FromContext(ctx)
	log := r.Log.WithValues("Gateway", req.NamespacedName)
	r.ctx = ctx

	var gateway networkingv1alpha1.Gateway

	if err := r.Get(ctx, req.NamespacedName, &gateway); err != nil {
		if util.IsNotFound(err) {
			log.V(1).Info("Gateway deleted", "error", err)
		}
		return ctrl.Result{}, util.IgnoreNotFound(err)
	}

	if err := r.createOrUpdateGateway(&gateway); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *GatewayReconciler) createOrUpdateGateway(gateway *networkingv1alpha1.Gateway) error {
	defer r.saveGatewayStatus(gateway)
	log := r.Log.WithName("createOrUpdateGateway")

	k8sGateway := k8sgatewayapiv1alpha2.Gateway{}
	key := client.ObjectKey{}

	if gateway.Spec.GatewayRef != nil {
		key = client.ObjectKey{Namespace: gateway.Spec.GatewayRef.Namespace, Name: gateway.Spec.GatewayRef.Name}
		if err := r.Get(r.ctx, key, &k8sGateway); err != nil {
			log.Error(err, "Failed to get k8s Gateway",
				"namespace", gateway.Spec.GatewayRef.Namespace, "name", gateway.Spec.GatewayRef.Name)
			reason := k8sgatewayapiv1alpha2.GatewayReasonNotReconciled
			if util.IsNotFound(err) {
				reason = networkingv1alpha1.GatewayReasonNotFound
			}
			condition := metav1.Condition{
				Type:               string(k8sgatewayapiv1alpha2.GatewayConditionScheduled),
				Status:             metav1.ConditionFalse,
				ObservedGeneration: gateway.Generation,
				Reason:             string(reason),
				Message:            err.Error(),
			}
			gateway.Status.Conditions = nil
			gateway.Status.Conditions = append(gateway.Status.Conditions, condition)
			return err
		} else {
			r.k8sGateway = &k8sGateway
			return r.reconcileK8sGateway(gateway, k8sGateway)
		}
	}

	if gateway.Spec.GatewayDef != nil {
		key = client.ObjectKey{Namespace: gateway.Spec.GatewayDef.Namespace, Name: gateway.Spec.GatewayDef.Name}
		if err := r.Get(r.ctx, key, &k8sGateway); err == nil {
			r.k8sGateway = &k8sGateway
			return r.reconcileK8sGateway(gateway, k8sGateway)
		} else if util.IsNotFound(err) {
			return r.createK8sGateway(gateway)
		} else {
			log.Error(err, "Failed to reconcile k8s Gateway",
				"namespace", gateway.Spec.GatewayDef.Namespace, "name", gateway.Spec.GatewayDef.Name)
			condition := metav1.Condition{
				Type:               string(k8sgatewayapiv1alpha2.GatewayConditionScheduled),
				Status:             metav1.ConditionFalse,
				ObservedGeneration: gateway.Generation,
				Reason:             string(k8sgatewayapiv1alpha2.GatewayReasonNotReconciled),
				Message:            err.Error(),
			}
			gateway.Status.Conditions = nil
			gateway.Status.Conditions = append(gateway.Status.Conditions, condition)
			return err
		}
	}

	return nil
}

func (r *GatewayReconciler) createK8sGateway(gateway *networkingv1alpha1.Gateway) error {
	log := r.Log.WithName("createK8sGateway")
	gatewayClassName := gateway.Spec.GatewayDef.GatewayClassName
	if !networkingv1alpha1.GatewayCompatibilities[string(gatewayClassName)]["allowMultipleGateway"] {
		k8sGateways := k8sgatewayapiv1alpha2.GatewayList{}
		if err := r.List(r.ctx, &k8sGateways, client.MatchingFields{"gatewayClassName": string(gatewayClassName)}); err != nil {
			log.Error(err, "Failed to list k8s Gateway", "gatewayClassName", gatewayClassName)
			return err
		}
		if len(k8sGateways.Items) > 0 {
			err := errors.New(fmt.Sprintf("an older Gateway exists for the accepted gatewayClassName: %s", gatewayClassName))
			log.Error(err, "Failed to create k8s Gateway",
				"namespace", gateway.Spec.GatewayDef.Namespace, "name", gateway.Spec.GatewayDef.Name)
			condition := metav1.Condition{
				Type:               string(k8sgatewayapiv1alpha2.GatewayConditionScheduled),
				Status:             metav1.ConditionFalse,
				ObservedGeneration: gateway.Generation,
				Reason:             string(networkingv1alpha1.GatewayReasonExists),
				Message:            err.Error(),
			}
			gateway.Status.Conditions = nil
			gateway.Status.Conditions = append(gateway.Status.Conditions, condition)
			return err
		}
	}

	k8sGateway := &k8sgatewayapiv1alpha2.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      gateway.Spec.GatewayDef.Name,
			Namespace: gateway.Spec.GatewayDef.Namespace,
		},
		Spec: k8sgatewayapiv1alpha2.GatewaySpec{
			GatewayClassName: gateway.Spec.GatewayDef.GatewayClassName,
			Listeners:        gateway.Spec.GatewaySpec.Listeners,
		},
	}
	k8sGateway.SetOwnerReferences(nil)
	if err := ctrl.SetControllerReference(gateway, k8sGateway, r.Scheme); err != nil {
		log.Error(err, "Failed to SetOwnerReferences for k8s Gateway")
		return err
	}
	if err := r.Create(r.ctx, k8sGateway); err != nil {
		log.Error(err, "Failed to create k8s Gateway",
			"namespace", gateway.Spec.GatewayDef.Namespace, "name", gateway.Spec.GatewayDef.Name)
		condition := metav1.Condition{
			Type:               string(k8sgatewayapiv1alpha2.GatewayConditionScheduled),
			Status:             metav1.ConditionFalse,
			ObservedGeneration: gateway.Generation,
			Reason:             string(networkingv1alpha1.GatewayReasonCreationFailure),
			Message:            err.Error(),
		}
		gateway.Status.Conditions = append(gateway.Status.Conditions, condition)
		return err
	}

	condition := metav1.Condition{
		Type:               string(k8sgatewayapiv1alpha2.GatewayConditionScheduled),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: gateway.Generation,
		Reason:             string(networkingv1alpha1.GatewayReasonResourcesAvailable),
		Message:            "Deployed k8s gateway to the cluster",
	}
	gateway.Status.Conditions = append(gateway.Status.Conditions, condition)
	log.Info("K8s Gateway Deployed",
		"namespace", gateway.Spec.GatewayDef.Namespace, "name", gateway.Spec.GatewayDef.Name)

	return nil
}

func (r *GatewayReconciler) reconcileK8sGateway(gateway *networkingv1alpha1.Gateway, k8sGateway k8sgatewayapiv1alpha2.Gateway) error {
	log := r.Log.WithName("reconcileK8sGateway")
	var oldListeners []k8sgatewayapiv1alpha2.Listener

	gatewayListenersAnnotation := []byte(gateway.Annotations[networkingv1alpha1.GatewayListenersAnnotation])
	if err := json.Unmarshal(gatewayListenersAnnotation, oldListeners); err != nil {
		return err
	}
	oldListenersMapping := convertListenersListToMapping(oldListeners)
	newListenersMapping := convertListenersListToMapping(gateway.Spec.GatewaySpec.Listeners)
	k8sGatewayListenersMapping := convertListenersListToMapping(k8sGateway.Spec.Listeners)

	for name, _ := range oldListenersMapping {
		if _, ok := newListenersMapping[name]; !ok {
			delete(k8sGatewayListenersMapping, name)
		}
	}

	for name, listener := range newListenersMapping {
		k8sGatewayListenersMapping[name] = listener
	}

	k8sGatewayListeners := convertListenersMappingToList(k8sGatewayListenersMapping)
	k8sGateway.Spec.Listeners = k8sGatewayListeners
	if err := r.Update(r.ctx, &k8sGateway); err != nil {
		log.Error(err, "Failed to reconcile k8s Gateway",
			"namespace", gateway.Spec.GatewayDef.Namespace, "name", gateway.Spec.GatewayDef.Name)
		condition := metav1.Condition{
			Type:               string(k8sgatewayapiv1alpha2.GatewayConditionScheduled),
			Status:             metav1.ConditionFalse,
			ObservedGeneration: gateway.Generation,
			Reason:             string(k8sgatewayapiv1alpha2.GatewayReasonNotReconciled),
			Message:            err.Error(),
		}
		gateway.Status.Conditions = nil
		gateway.Status.Conditions = append(gateway.Status.Conditions, condition)
		return err
	}

	listenersAnnotation, _ := json.Marshal(gateway.Spec.GatewaySpec.Listeners)
	gateway.Annotations[networkingv1alpha1.GatewayListenersAnnotation] = string(listenersAnnotation)
	if err := r.Update(r.ctx, gateway); err != nil {
		return err
	}
	return nil
}

func (r *GatewayReconciler) finalize(gateway *networkingv1alpha1.Gateway) error {
	return nil
}

func (r *GatewayReconciler) syncSatusFromK8sGateway(gateway *networkingv1alpha1.Gateway) error {
	return nil
}

func (r *GatewayReconciler) createOrUpdateService(gateway *networkingv1alpha1.Gateway) error {
	//log := r.Log.WithName("createOrUpdateService")
	return nil
}

func (r *GatewayReconciler) saveGatewayStatus(gateway *networkingv1alpha1.Gateway) {
	log := r.Log.WithName("saveGatewayStatus")
	if err := r.Status().Update(r.ctx, gateway); err != nil {
		log.Error(err, "Failed to update status on Gateway",
			"namespace", gateway.Namespace,
			"name", gateway.Name)
	} else {
		log.Info("Updated status on Gateway", "resource version", gateway.ResourceVersion)
	}
}

func convertListenersListToMapping(listeners []k8sgatewayapiv1alpha2.Listener) map[k8sgatewayapiv1alpha2.SectionName]k8sgatewayapiv1alpha2.Listener {
	var mapping map[k8sgatewayapiv1alpha2.SectionName]k8sgatewayapiv1alpha2.Listener
	for _, listener := range listeners {
		mapping[listener.Name] = listener
	}
	return mapping
}

func convertListenersMappingToList(mapping map[k8sgatewayapiv1alpha2.SectionName]k8sgatewayapiv1alpha2.Listener) []k8sgatewayapiv1alpha2.Listener {
	var listeners []k8sgatewayapiv1alpha2.Listener
	for _, listener := range mapping {
		listeners = append(listeners, listener)
	}
	return listeners
}

// SetupWithManager sets up the controller with the Manager.
func (r *GatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&networkingv1alpha1.Gateway{}).
		Complete(r)
}
