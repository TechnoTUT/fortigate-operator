package controller

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/TechnoTUT/fortigate-operator/api/v1alpha1"
	"github.com/TechnoTUT/fortigate-operator/internal/fortigate"
)

// namedKind describes a resource whose FortiGate table is keyed by name
// (firewall/address, firewall.service/custom).
type namedKind[T client.Object] struct {
	kind         string
	path         string
	commentField string
	maxNameLen   int
	newObject    func() T
	common       func(T) v1alpha1.CommonSpec
	status       func(T) *v1alpha1.ObjectStatus
	comment      func(T) string
	// desired returns the fields to write, without name and comment.
	desired func(T) (fortigate.Object, error)
}

// NamedReconciler reconciles a name-keyed kind.
type NamedReconciler[T client.Object] struct {
	client.Client
	Options
	k namedKind[T]
}

// SetupWithManager registers the reconciler.
func (r *NamedReconciler[T]) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(r.k.newObject(), builder.WithPredicates(specChanged())).
		Named(strings.ToLower(r.k.kind)).
		Complete(r)
}

// Reconcile implements reconcile.Reconciler.
func (r *NamedReconciler[T]) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	obj := r.k.newObject()
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	name := fortiName(obj, r.k.common(obj))
	if !obj.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, r.finalize(ctx, obj, name)
	}
	if controllerutil.AddFinalizer(obj, Finalizer) {
		if err := r.Update(ctx, obj); err != nil {
			return ctrl.Result{}, err
		}
	}

	orig := obj.DeepCopyObject().(T)
	syncErr := r.sync(ctx, obj, name)
	st := r.k.status(obj)
	st.Name = name
	st.LastSyncTime = now()
	if syncErr == nil {
		st.ObservedGeneration = obj.GetGeneration()
	}
	if err := r.Status().Patch(ctx, obj, client.MergeFrom(orig)); err != nil {
		return ctrl.Result{}, err
	}
	if syncErr != nil {
		return ctrl.Result{}, syncErr
	}
	return ctrl.Result{RequeueAfter: r.Resync}, nil
}

// sync makes the FortiGate object match the spec and records the outcome in the Ready condition.
// It returns an error only for failures worth retrying soon.
func (r *NamedReconciler[T]) sync(ctx context.Context, obj T, name string) error {
	logger := log.FromContext(ctx)
	conds := &r.k.status(obj).Conditions
	gen := obj.GetGeneration()
	ready := func(s metav1.ConditionStatus, reason, msg string) { setReady(conds, gen, s, reason, msg) }

	if len(name) > r.k.maxNameLen {
		ready(metav1.ConditionFalse, v1alpha1.ReasonInvalid, fmt.Sprintf("name %q is longer than %d characters", name, r.k.maxNameLen))
		return nil
	}
	want, err := r.k.desired(obj)
	if err != nil {
		ready(metav1.ConditionFalse, v1alpha1.ReasonInvalid, err.Error())
		return nil
	}
	want[r.k.commentField] = withMarker(obj, r.k.comment(obj))

	have, err := r.FortiGate.Get(ctx, r.k.path, name)
	if fortigate.IsNotFound(err) {
		if r.DryRun {
			logger.Info("dry run: would create", "path", r.k.path, "name", name, "body", want)
			ready(metav1.ConditionFalse, v1alpha1.ReasonDryRun, "would create the object")
			return nil
		}
		body := fortigate.Object{"name": name}
		for k, v := range want {
			body[k] = v
		}
		if _, err := r.FortiGate.Create(ctx, r.k.path, body); err != nil {
			ready(metav1.ConditionFalse, v1alpha1.ReasonAPIError, err.Error())
			return err
		}
		logger.Info("created", "path", r.k.path, "name", name)
		return r.verify(ctx, obj, name, want, ready)
	}
	if err != nil {
		ready(metav1.ConditionFalse, v1alpha1.ReasonAPIError, err.Error())
		return err
	}
	if why := ownership(obj, scalarString(have[r.k.commentField])); why != "" {
		ready(metav1.ConditionFalse, v1alpha1.ReasonConflict, why)
		return nil
	}
	changed := diff(want, have)
	if len(changed) == 0 {
		ready(metav1.ConditionTrue, v1alpha1.ReasonSynced, "the FortiGate object matches the spec")
		return nil
	}
	if r.DryRun {
		logger.Info("dry run: would update", "path", r.k.path, "name", name, "fields", changed)
		ready(metav1.ConditionFalse, v1alpha1.ReasonDryRun, "would update "+strings.Join(changed, ", "))
		return nil
	}
	body := fortigate.Object{}
	for _, k := range changed {
		body[k] = want[k]
	}
	if err := r.FortiGate.Update(ctx, r.k.path, name, body); err != nil {
		ready(metav1.ConditionFalse, v1alpha1.ReasonAPIError, err.Error())
		return err
	}
	logger.Info("updated", "path", r.k.path, "name", name, "fields", changed)
	return r.verify(ctx, obj, name, want, ready)
}

// verify reads the object back after a write. A field that the FortiGate stores differently
// from what was written would otherwise be rewritten on every resync without anyone noticing.
func (r *NamedReconciler[T]) verify(ctx context.Context, _ T, name string, want fortigate.Object, ready func(metav1.ConditionStatus, string, string)) error {
	have, err := r.FortiGate.Get(ctx, r.k.path, name)
	if err != nil {
		ready(metav1.ConditionFalse, v1alpha1.ReasonAPIError, err.Error())
		return err
	}
	if d := diff(want, have); len(d) > 0 {
		ready(metav1.ConditionFalse, v1alpha1.ReasonNotConverged, "the FortiGate stored different values for "+strings.Join(d, ", "))
		return nil
	}
	ready(metav1.ConditionTrue, v1alpha1.ReasonSynced, "the FortiGate object matches the spec")
	return nil
}

func (r *NamedReconciler[T]) finalize(ctx context.Context, obj T, name string) error {
	if !controllerutil.ContainsFinalizer(obj, Finalizer) {
		return nil
	}
	logger := log.FromContext(ctx)
	if r.k.common(obj).DeletionPolicy != v1alpha1.DeletionPolicyOrphan {
		have, err := r.FortiGate.Get(ctx, r.k.path, name)
		switch {
		case fortigate.IsNotFound(err):
		case err != nil:
			return err
		case ownership(obj, scalarString(have[r.k.commentField])) != "":
			logger.Info("not deleting a FortiGate object that this resource does not own", "path", r.k.path, "name", name)
		case r.DryRun:
			logger.Info("dry run: would delete", "path", r.k.path, "name", name)
		default:
			if err := r.FortiGate.Delete(ctx, r.k.path, name); err != nil && !fortigate.IsNotFound(err) {
				orig := obj.DeepCopyObject().(T)
				st := r.k.status(obj)
				setReady(&st.Conditions, obj.GetGeneration(), metav1.ConditionFalse, v1alpha1.ReasonDeleteFail, err.Error())
				_ = r.Status().Patch(ctx, obj, client.MergeFrom(orig))
				return err
			}
			logger.Info("deleted", "path", r.k.path, "name", name)
		}
	}
	controllerutil.RemoveFinalizer(obj, Finalizer)
	return r.Update(ctx, obj)
}
