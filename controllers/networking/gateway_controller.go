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
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	k8sgatewayapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	networkingv1alpha1 "github.com/openfunction/apis/networking/v1alpha1"
	"github.com/openfunction/pkg/util"
)

const (
	GatewayServiceName        = "gateway"
	IstioGatewayClassName     = "istio"
	ContourGatewayClassName   = "contour"
	ContourGatewayServiceName = "envoy"
	GatewayFinalizerName      = "networking.openfunction.io/finalizer"
	GatewayIndexField         = ".spec.gatewayRef.Index"
	K8sGatewayIndexField      = ".spec.gatewayClassName"
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
//+kubebuilder:rbac:groups=networking.k8s.io,resources=gateways,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.k8s.io,resources=gateways/status,verbs=get;update;patch
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

	gateway := &networkingv1alpha1.Gateway{}

	if err := r.Get(ctx, req.NamespacedName, gateway); err != nil {
		if util.IsNotFound(err) {
			log.V(1).Info("Gateway deleted", "error", err)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if gateway.ObjectMeta.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(gateway, GatewayFinalizerName) {
			controllerutil.AddFinalizer(gateway, GatewayFinalizerName)
			if err := r.Update(ctx, gateway); err != nil {
				return ctrl.Result{}, err
			}
		}
	} else {
		if controllerutil.ContainsFinalizer(gateway, GatewayFinalizerName) {
			if err := r.cleanExternalResources(gateway); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(gateway, GatewayFinalizerName)
			if err := r.Update(ctx, gateway); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if err := r.createOrUpdateGateway(gateway); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.createOrUpdateService(gateway); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *GatewayReconciler) createOrUpdateGateway(gateway *networkingv1alpha1.Gateway) error {
	defer r.updateGatewayStatus(gateway.Status.DeepCopy(), gateway)
	log := r.Log.WithName("createOrUpdateGateway")
	k8sGateway := &k8sgatewayapiv1alpha2.Gateway{}

	if gateway.Spec.GatewayRef != nil {
		key := client.ObjectKey{Namespace: gateway.Spec.GatewayRef.Namespace, Name: gateway.Spec.GatewayRef.Name}
		if err := r.Get(r.ctx, key, k8sGateway); err != nil {
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
				LastTransitionTime: metav1.Now(),
				Reason:             string(reason),
				Message:            err.Error(),
			}
			gateway.Status.Conditions = nil
			gateway.Status.Conditions = append(gateway.Status.Conditions, condition)
			return err
		} else {
			r.k8sGateway = k8sGateway
			if r.needReconcileK8sGateway(gateway) {
				if err := r.reconcileK8sGateway(gateway); err != nil {
					return err
				}
			}
		}
	}

	if gateway.Spec.GatewayDef != nil {
		key := client.ObjectKey{Namespace: gateway.Spec.GatewayDef.Namespace, Name: gateway.Spec.GatewayDef.Name}
		if err := r.Get(r.ctx, key, k8sGateway); err == nil {
			r.k8sGateway = k8sGateway
			if r.needReconcileK8sGateway(gateway) {
				if err := r.reconcileK8sGateway(gateway); err != nil {
					return err
				}
			}
		} else if util.IsNotFound(err) {
			if err := r.createK8sGateway(gateway); err != nil {
				return err
			}
		} else {
			log.Error(err, "Failed to reconcile k8s Gateway",
				"namespace", gateway.Spec.GatewayDef.Namespace, "name", gateway.Spec.GatewayDef.Name)
			condition := metav1.Condition{
				Type:               string(k8sgatewayapiv1alpha2.GatewayConditionScheduled),
				Status:             metav1.ConditionFalse,
				ObservedGeneration: gateway.Generation,
				LastTransitionTime: metav1.Now(),
				Reason:             string(k8sgatewayapiv1alpha2.GatewayReasonNotReconciled),
				Message:            err.Error(),
			}
			gateway.Status.Conditions = nil
			gateway.Status.Conditions = append(gateway.Status.Conditions, condition)
			return err
		}
	}

	if r.k8sGateway != nil {
		r.syncStatusFromK8sGateway(gateway)
	}

	return nil
}

func (r *GatewayReconciler) createK8sGateway(gateway *networkingv1alpha1.Gateway) error {
	log := r.Log.WithName("createK8sGateway")
	gatewayClassName := gateway.Spec.GatewayDef.GatewayClassName
	if !networkingv1alpha1.GatewayCompatibilities[string(gatewayClassName)]["allowMultipleGateway"] {
		k8sGateways := k8sgatewayapiv1alpha2.GatewayList{}
		listOps := &client.ListOptions{
			FieldSelector: fields.OneTermEqualSelector(K8sGatewayIndexField, string(gatewayClassName)),
		}
		if err := r.List(r.ctx, &k8sGateways, listOps); err != nil {
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
				LastTransitionTime: metav1.Now(),
				Reason:             string(networkingv1alpha1.GatewayReasonExists),
				Message:            err.Error(),
			}
			gateway.Status.Conditions = nil
			gateway.Status.Conditions = append(gateway.Status.Conditions, condition)
			return err
		}
	}

	listenersAnnotation, _ := json.Marshal(gateway.Spec.GatewaySpec.Listeners)
	k8sGateway := &k8sgatewayapiv1alpha2.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:        gateway.Spec.GatewayDef.Name,
			Namespace:   gateway.Spec.GatewayDef.Namespace,
			Annotations: map[string]string{networkingv1alpha1.GatewayListenersAnnotation: string(listenersAnnotation)},
		},
		Spec: k8sgatewayapiv1alpha2.GatewaySpec{
			GatewayClassName: gateway.Spec.GatewayDef.GatewayClassName,
			Listeners:        gateway.Spec.GatewaySpec.Listeners,
		},
	}

	if err := r.Create(r.ctx, k8sGateway); err != nil {
		log.Error(err, "Failed to create k8s Gateway",
			"namespace", gateway.Spec.GatewayDef.Namespace, "name", gateway.Spec.GatewayDef.Name)
		condition := metav1.Condition{
			Type:               string(k8sgatewayapiv1alpha2.GatewayConditionScheduled),
			Status:             metav1.ConditionFalse,
			ObservedGeneration: gateway.Generation,
			LastTransitionTime: metav1.Now(),
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
		LastTransitionTime: metav1.Now(),
		Reason:             string(networkingv1alpha1.GatewayReasonResourcesAvailable),
		Message:            "Deployed k8s gateway to the cluster",
	}
	gateway.Status.Conditions = append(gateway.Status.Conditions, condition)
	log.Info("K8s Gateway Deployed",
		"namespace", gateway.Spec.GatewayDef.Namespace, "name", gateway.Spec.GatewayDef.Name)
	return nil
}

func (r *GatewayReconciler) reconcileK8sGateway(gateway *networkingv1alpha1.Gateway) error {
	log := r.Log.WithName("reconcileK8sGateway")
	var oldListeners []k8sgatewayapiv1alpha2.Listener

	if r.k8sGateway.Annotations == nil {
		r.k8sGateway.Annotations = make(map[string]string)
	}
	gatewayListenersAnnotation := []byte(r.k8sGateway.Annotations[networkingv1alpha1.GatewayListenersAnnotation])
	if err := json.Unmarshal(gatewayListenersAnnotation, &oldListeners); err != nil {
		log.Error(err, "Failed to Unmarshal GatewayListenersAnnotation")
	}
	oldListenersMapping := convertListenersListToMapping(oldListeners)
	newListenersMapping := convertListenersListToMapping(gateway.Spec.GatewaySpec.Listeners)
	k8sGatewayListenersMapping := convertListenersListToMapping(r.k8sGateway.Spec.Listeners)
	for name := range oldListenersMapping {
		if _, ok := newListenersMapping[name]; !ok {
			delete(k8sGatewayListenersMapping, name)
		}
	}
	for name, listener := range newListenersMapping {
		k8sGatewayListenersMapping[name] = listener
	}
	k8sGatewayListeners := convertListenersMappingToList(k8sGatewayListenersMapping)
	r.k8sGateway.Spec.Listeners = k8sGatewayListeners
	listenersAnnotation, _ := json.Marshal(gateway.Spec.GatewaySpec.Listeners)
	r.k8sGateway.Annotations[networkingv1alpha1.GatewayListenersAnnotation] = string(listenersAnnotation)

	if err := r.Update(r.ctx, r.k8sGateway); err != nil {
		log.Error(err, "Failed to reconcile k8s Gateway",
			"namespace", r.k8sGateway.Namespace, "name", r.k8sGateway.Name)
		condition := metav1.Condition{
			Type:               string(k8sgatewayapiv1alpha2.GatewayConditionScheduled),
			Status:             metav1.ConditionFalse,
			ObservedGeneration: gateway.Generation,
			LastTransitionTime: metav1.Now(),
			Reason:             string(k8sgatewayapiv1alpha2.GatewayReasonNotReconciled),
			Message:            err.Error(),
		}
		gateway.Status.Conditions = nil
		gateway.Status.Conditions = append(gateway.Status.Conditions, condition)
		return err
	}
	return nil
}

func (r *GatewayReconciler) cleanExternalResources(gateway *networkingv1alpha1.Gateway) error {
	log := r.Log.WithName("cleanExternalResources")
	if gateway.Spec.GatewayRef != nil {
		k8sGateway := &k8sgatewayapiv1alpha2.Gateway{}
		key := client.ObjectKey{Namespace: gateway.Spec.GatewayRef.Namespace, Name: gateway.Spec.GatewayRef.Name}
		if err := r.Get(r.ctx, key, k8sGateway); err != nil {
			if !util.IsNotFound(err) {
				log.Error(err, "Failed to get k8s gateway",
					"namespace", gateway.Spec.GatewayRef.Namespace, "name", gateway.Spec.GatewayRef.Name)
			}
			return util.IgnoreNotFound(err)
		}
		if k8sGateway.Annotations == nil {
			return nil
		}
		var needRemoveListeners []k8sgatewayapiv1alpha2.Listener
		gatewayListenersAnnotation := []byte(k8sGateway.Annotations[networkingv1alpha1.GatewayListenersAnnotation])
		if err := json.Unmarshal(gatewayListenersAnnotation, &needRemoveListeners); err != nil {
			return nil
		}
		needRemoveListenersMapping := convertListenersListToMapping(needRemoveListeners)
		k8sGatewayListenersMapping := convertListenersListToMapping(k8sGateway.Spec.Listeners)
		for name := range needRemoveListenersMapping {
			delete(k8sGatewayListenersMapping, name)
		}
		k8sGatewayListeners := convertListenersMappingToList(k8sGatewayListenersMapping)
		k8sGateway.Spec.Listeners = k8sGatewayListeners
		delete(k8sGateway.Annotations, networkingv1alpha1.GatewayListenersAnnotation)
		if err := r.Update(r.ctx, k8sGateway); err != nil {
			log.Error(err, "Failed to clean k8s Gateway",
				"namespace", gateway.Spec.GatewayRef.Namespace, "name", gateway.Spec.GatewayRef.Name)
			return err
		}
	}

	if gateway.Spec.GatewayDef != nil && r.k8sGateway != nil {
		if err := r.Delete(r.ctx, r.k8sGateway); err != nil {
			if !util.IsNotFound(err) {
				log.Error(err, "Failed to clean k8s Gateway",
					"namespace", gateway.Spec.GatewayDef.Namespace, "name", gateway.Spec.GatewayDef.Name)
			}
			return util.IgnoreNotFound(err)
		}
	}
	return nil
}

func (r *GatewayReconciler) needReconcileK8sGateway(gateway *networkingv1alpha1.Gateway) bool {
	gatewayListeners := convertListenersListToMapping(gateway.Spec.GatewaySpec.Listeners)
	k8sGatewayListeners := convertListenersListToMapping(r.k8sGateway.Spec.Listeners)
	for name, gatewayListener := range gatewayListeners {
		if k8sGatewayListener, ok := k8sGatewayListeners[name]; !ok || !equality.Semantic.DeepEqual(gatewayListener, k8sGatewayListener) {
			return true
		}
	}
	if r.k8sGateway.Annotations == nil {
		return true
	}
	var annotationListeners []k8sgatewayapiv1alpha2.Listener
	gatewayListenersAnnotation := []byte(r.k8sGateway.Annotations[networkingv1alpha1.GatewayListenersAnnotation])
	if err := json.Unmarshal(gatewayListenersAnnotation, &annotationListeners); err != nil {
		return true
	}
	if !!equality.Semantic.DeepEqual(annotationListeners, gatewayListeners) {
		return true
	}
	return false
}

func (r *GatewayReconciler) syncStatusFromK8sGateway(gateway *networkingv1alpha1.Gateway) {
	gateway.Status.Conditions = r.k8sGateway.Status.Conditions
	gatewayListeners := convertListenersListToMapping(gateway.Spec.GatewaySpec.Listeners)
	var refreshedGatewayListeners []k8sgatewayapiv1alpha2.ListenerStatus
	for _, gatewayListener := range r.k8sGateway.Status.Listeners {
		if _, ok := gatewayListeners[gatewayListener.Name]; ok {
			refreshedGatewayListeners = append(refreshedGatewayListeners, gatewayListener)
		}
	}
	gateway.Status.Listeners = refreshedGatewayListeners
	gateway.Status.Addresses = r.k8sGateway.Status.Addresses

}

func (r *GatewayReconciler) createOrUpdateService(gateway *networkingv1alpha1.Gateway) error {
	log := r.Log.WithName("createOrUpdateService")
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: gateway.Namespace, Name: GatewayServiceName},
	}
	op, err := controllerutil.CreateOrUpdate(r.ctx, r.Client, service, r.mutateService(gateway, service))
	if err != nil {
		log.Error(err, "Failed to CreateOrUpdate service")
		return err
	}
	log.V(1).Info(fmt.Sprintf("Service %s", op))
	return nil
}

func (r *GatewayReconciler) mutateService(gateway *networkingv1alpha1.Gateway, service *corev1.Service) controllerutil.MutateFn {
	return func() error {
		if r.k8sGateway != nil {
			var servicePorts []corev1.ServicePort
			var externalName string
			for _, listener := range gateway.Spec.GatewaySpec.Listeners {
				if !strings.HasSuffix(string(*listener.Hostname), gateway.Spec.ClusterDomain) {
					servicePort := corev1.ServicePort{
						Name:       string(listener.Name),
						Protocol:   corev1.ProtocolTCP,
						Port:       int32(listener.Port),
						TargetPort: intstr.IntOrString{Type: intstr.Int, IntVal: int32(listener.Port)},
					}
					servicePorts = append(servicePorts, servicePort)
				}
			}
			if networkingv1alpha1.GatewayCompatibilities[string(r.k8sGateway.Spec.GatewayClassName)]["implementedAddresses"] {
				externalName = fmt.Sprintf("%s.%s.svc.%s",
					r.k8sGateway.Name, r.k8sGateway.Namespace, gateway.Spec.ClusterDomain)
			} else if string(r.k8sGateway.Spec.GatewayClassName) == ContourGatewayClassName {
				externalName = fmt.Sprintf("%s.%s.svc.%s",
					ContourGatewayServiceName, r.k8sGateway.Namespace, gateway.Spec.ClusterDomain)
			}
			service.Spec.Type = corev1.ServiceTypeExternalName
			service.Spec.Ports = servicePorts
			service.Spec.ExternalName = externalName
			return ctrl.SetControllerReference(gateway, service, r.Scheme)
		}
		return nil
	}
}

func (r *GatewayReconciler) updateGatewayStatus(oldStatus *networkingv1alpha1.GatewayStatus, gateway *networkingv1alpha1.Gateway) {
	log := r.Log.WithName("updateGatewayStatus")
	if !equality.Semantic.DeepEqual(oldStatus, gateway.Status) {
		if err := r.Status().Update(r.ctx, gateway); err != nil {
			log.Error(err, "Failed to update status on Gateway", "namespace", gateway.Namespace, "name", gateway.Name)
		}
		log.Info("Updated status on Gateway", "namespace", gateway.Namespace,
			"name", gateway.Name, "resource version", gateway.ResourceVersion)
	}
}

func convertListenersListToMapping(listeners []k8sgatewayapiv1alpha2.Listener) map[k8sgatewayapiv1alpha2.SectionName]k8sgatewayapiv1alpha2.Listener {
	mapping := make(map[k8sgatewayapiv1alpha2.SectionName]k8sgatewayapiv1alpha2.Listener)
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
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &networkingv1alpha1.Gateway{}, GatewayIndexField, func(rawObj client.Object) []string {
		gateway := rawObj.(*networkingv1alpha1.Gateway)
		if gateway.Spec.GatewayRef != nil {
			return []string{fmt.Sprintf("%s,%s", gateway.Spec.GatewayRef.Namespace, gateway.Spec.GatewayRef.Name)}
		}
		if gateway.Spec.GatewayDef != nil {
			return []string{fmt.Sprintf("%s,%s", gateway.Spec.GatewayDef.Namespace, gateway.Spec.GatewayDef.Name)}
		}
		return nil
	}); err != nil {
		return err
	}

	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &k8sgatewayapiv1alpha2.Gateway{}, K8sGatewayIndexField, func(rawObj client.Object) []string {
		k8sGateway := rawObj.(*k8sgatewayapiv1alpha2.Gateway)
		return []string{string(k8sGateway.Spec.GatewayClassName)}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&networkingv1alpha1.Gateway{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(
			&source.Kind{Type: &k8sgatewayapiv1alpha2.Gateway{}},
			handler.EnqueueRequestsFromMapFunc(r.findObjectsForK8sGateway),
		).
		Complete(r)
}

func (r *GatewayReconciler) findObjectsForK8sGateway(k8sGateway client.Object) []reconcile.Request {
	attachedGateways := &networkingv1alpha1.GatewayList{}
	listOps := &client.ListOptions{
		FieldSelector: fields.OneTermEqualSelector(GatewayIndexField, fmt.Sprintf("%s,%s", k8sGateway.GetNamespace(), k8sGateway.GetName())),
	}
	err := r.List(context.TODO(), attachedGateways, listOps)
	if err != nil {
		return []reconcile.Request{}
	}
	requests := make([]reconcile.Request, len(attachedGateways.Items))
	for i, item := range attachedGateways.Items {
		requests[i] = reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      item.GetName(),
				Namespace: item.GetNamespace(),
			},
		}
	}
	return requests
}
