package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/TechnoTUT/fortigate-operator/api/v1alpha1"
	"github.com/TechnoTUT/fortigate-operator/internal/fortigate"
)

const (
	policyPath       = "firewall/policy"
	policyMaxNameLen = 35
)

// PolicyReconciler reconciles FirewallPolicy.
//
// Policies are keyed by a number (policyid) on the FortiGate, so they are looked up by name.
// The whole policy list is read on every reconcile, which also gives the evaluation order.
type PolicyReconciler struct {
	client.Client
	Options
}

// NewPolicyReconciler returns the reconciler for FirewallPolicy.
func NewPolicyReconciler(c client.Client, o Options) *PolicyReconciler {
	return &PolicyReconciler{Client: c, Options: o}
}

// SetupWithManager registers the reconciler. Policies are reconciled one at a time, because
// moving one policy changes the order the others see.
func (r *PolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.FirewallPolicy{}, builder.WithPredicates(specChanged())).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Named("firewallpolicy").
		Complete(r)
}

// policyFields returns the fields to write, without name.
func policyFields(p *v1alpha1.FirewallPolicy) fortigate.Object {
	s := p.Spec
	def := func(v, d string) string {
		if v == "" {
			return d
		}
		return v
	}
	return fortigate.Object{
		"srcintf":    nameList(s.SrcIntf),
		"dstintf":    nameList(s.DstIntf),
		"srcaddr":    nameList(s.SrcAddr),
		"dstaddr":    nameList(s.DstAddr),
		"service":    nameList(s.Service),
		"action":     def(s.Action, "accept"),
		"schedule":   def(s.Schedule, "always"),
		"nat":        def(s.NAT, "disable"),
		"status":     def(s.Status, "enable"),
		"logtraffic": def(s.LogTraffic, "utm"),
		"comments":   withMarker(p, s.Comments),
	}
}

// policyEntry is a policy in the list read from the FortiGate.
type policyEntry struct {
	index int
	id    int
	obj   fortigate.Object
}

func findPolicy(list []fortigate.Object, name string) (policyEntry, bool) {
	for i, o := range list {
		if n, _ := o["name"].(string); n == name {
			id, _ := strconv.Atoi(scalarString(o["policyid"]))
			return policyEntry{index: i, id: id, obj: o}, true
		}
	}
	return policyEntry{}, false
}

// Reconcile implements reconcile.Reconciler.
func (r *PolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	p := &v1alpha1.FirewallPolicy{}
	if err := r.Get(ctx, req.NamespacedName, p); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	name := fortiName(p, p.Spec.CommonSpec)
	if !p.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalize(ctx, p, name)
	}
	if controllerutil.AddFinalizer(p, Finalizer) {
		if err := r.Update(ctx, p); err != nil {
			return ctrl.Result{}, err
		}
	}

	orig := p.DeepCopy()
	res, syncErr := r.sync(ctx, p, name)
	p.Status.Name = name
	p.Status.LastSyncTime = now()
	if syncErr == nil {
		p.Status.ObservedGeneration = p.Generation
	}
	if err := r.Status().Patch(ctx, p, client.MergeFrom(orig)); err != nil {
		return ctrl.Result{}, err
	}
	if syncErr != nil {
		return ctrl.Result{}, syncErr
	}
	if res.RequeueAfter == 0 {
		res.RequeueAfter = r.Resync
	}
	return res, nil
}

func (r *PolicyReconciler) sync(ctx context.Context, p *v1alpha1.FirewallPolicy, name string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	ready := func(s metav1.ConditionStatus, reason, msg string) {
		setReady(&p.Status.Conditions, p.Generation, s, reason, msg)
	}
	fail := func(err error) (ctrl.Result, error) {
		ready(metav1.ConditionFalse, v1alpha1.ReasonAPIError, err.Error())
		return ctrl.Result{}, err
	}

	if len(name) > policyMaxNameLen {
		ready(metav1.ConditionFalse, v1alpha1.ReasonInvalid, fmt.Sprintf("name %q is longer than %d characters", name, policyMaxNameLen))
		return ctrl.Result{}, nil
	}
	if pos := p.Spec.Position; pos != nil && (pos.Before == name || pos.After == name) {
		ready(metav1.ConditionFalse, v1alpha1.ReasonInvalid, "position refers to the policy itself")
		return ctrl.Result{}, nil
	}
	want := policyFields(p)

	list, err := r.FortiGate.List(ctx, policyPath)
	if err != nil {
		return fail(err)
	}
	cur, found := findPolicy(list, name)
	var actions []string
	if !found {
		if r.DryRun {
			logger.Info("dry run: would create", "path", policyPath, "name", name, "body", want)
			ready(metav1.ConditionFalse, v1alpha1.ReasonDryRun, "would create the policy")
			return ctrl.Result{}, nil
		}
		body := fortigate.Object{"policyid": 0, "name": name}
		for k, v := range want {
			body[k] = v
		}
		mkey, err := r.FortiGate.Create(ctx, policyPath, body)
		if err != nil {
			return fail(err)
		}
		logger.Info("created", "path", policyPath, "name", name, "policyid", mkey)
		actions = append(actions, "created")
	} else {
		p.Status.PolicyID = cur.id
		if why := ownership(p, scalarString(cur.obj["comments"])); why != "" {
			ready(metav1.ConditionFalse, v1alpha1.ReasonConflict, why)
			return ctrl.Result{}, nil
		}
		if changed := diff(want, cur.obj); len(changed) > 0 {
			if r.DryRun {
				logger.Info("dry run: would update", "path", policyPath, "name", name, "policyid", cur.id, "fields", changed)
				ready(metav1.ConditionFalse, v1alpha1.ReasonDryRun, "would update "+strings.Join(changed, ", "))
				return ctrl.Result{}, nil
			}
			body := fortigate.Object{}
			for _, k := range changed {
				body[k] = want[k]
			}
			if err := r.FortiGate.Update(ctx, policyPath, strconv.Itoa(cur.id), body); err != nil {
				return fail(err)
			}
			logger.Info("updated", "path", policyPath, "name", name, "policyid", cur.id, "fields", changed)
			actions = append(actions, "updated")
		}
	}

	if len(actions) > 0 {
		// Read the list again: the policy ID of a new policy and the stored values come from it.
		if list, err = r.FortiGate.List(ctx, policyPath); err != nil {
			return fail(err)
		}
		if cur, found = findPolicy(list, name); !found {
			return fail(fmt.Errorf("policy %q is missing after it was written", name))
		}
		p.Status.PolicyID = cur.id
	}

	if pos := p.Spec.Position; pos != nil {
		where, refName := "before", pos.Before
		if pos.After != "" {
			where, refName = "after", pos.After
		}
		ref, ok := findPolicy(list, refName)
		if !ok {
			ready(metav1.ConditionFalse, v1alpha1.ReasonNotFound, fmt.Sprintf("position: policy %q does not exist on the FortiGate", refName))
			return ctrl.Result{}, nil
		}
		misplaced := (where == "before" && cur.index > ref.index) || (where == "after" && cur.index < ref.index)
		if misplaced {
			if r.DryRun {
				logger.Info("dry run: would move", "name", name, where, refName)
				ready(metav1.ConditionFalse, v1alpha1.ReasonDryRun, fmt.Sprintf("would move the policy %s %q", where, refName))
				return ctrl.Result{}, nil
			}
			if err := r.FortiGate.Move(ctx, policyPath, strconv.Itoa(cur.id), where, strconv.Itoa(ref.id)); err != nil {
				return fail(err)
			}
			logger.Info("moved", "name", name, "policyid", cur.id, where, refName)
		}
	}

	if d := diff(want, cur.obj); len(d) > 0 {
		ready(metav1.ConditionFalse, v1alpha1.ReasonNotConverged, "the FortiGate stored different values for "+strings.Join(d, ", "))
		return ctrl.Result{}, nil
	}
	ready(metav1.ConditionTrue, v1alpha1.ReasonSynced, "the FortiGate policy matches the spec")
	return ctrl.Result{}, nil
}

func (r *PolicyReconciler) finalize(ctx context.Context, p *v1alpha1.FirewallPolicy, name string) error {
	if !controllerutil.ContainsFinalizer(p, Finalizer) {
		return nil
	}
	logger := log.FromContext(ctx)
	if p.Spec.DeletionPolicy != v1alpha1.DeletionPolicyOrphan {
		list, err := r.FortiGate.List(ctx, policyPath)
		if err != nil {
			return err
		}
		cur, found := findPolicy(list, name)
		switch {
		case !found:
		case ownership(p, scalarString(cur.obj["comments"])) != "":
			logger.Info("not deleting a policy that this resource does not own", "name", name, "policyid", cur.id)
		case r.DryRun:
			logger.Info("dry run: would delete", "name", name, "policyid", cur.id)
		default:
			if err := r.FortiGate.Delete(ctx, policyPath, strconv.Itoa(cur.id)); err != nil && !fortigate.IsNotFound(err) {
				orig := p.DeepCopy()
				setReady(&p.Status.Conditions, p.Generation, metav1.ConditionFalse, v1alpha1.ReasonDeleteFail, err.Error())
				_ = r.Status().Patch(ctx, p, client.MergeFrom(orig))
				return err
			}
			logger.Info("deleted", "name", name, "policyid", cur.id)
		}
	}
	controllerutil.RemoveFinalizer(p, Finalizer)
	return r.Update(ctx, p)
}
