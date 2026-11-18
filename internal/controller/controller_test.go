package controller

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/TechnoTUT/fortigate-operator/api/v1alpha1"
	"github.com/TechnoTUT/fortigate-operator/internal/fortigate"
)

func newClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(s).
		WithStatusSubresource(&v1alpha1.FirewallPolicy{}, &v1alpha1.FirewallAddress{}, &v1alpha1.FirewallService{}).
		WithObjects(objs...).Build()
}

func reconcileOnce(t *testing.T, r reconcile.Reconciler, ns, name string) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
	if err != nil {
		t.Fatalf("reconcile %s/%s: %v", ns, name, err)
	}
	return res
}

func readyOf(t *testing.T, conds []metav1.Condition) metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(conds, v1alpha1.ConditionReady)
	if c == nil {
		t.Fatal("no Ready condition")
	}
	return *c
}

func opts(fg *fakeFG) Options { return Options{FortiGate: fg, Resync: 5 * time.Minute} }

func address(name, subnet string) *v1alpha1.FirewallAddress {
	return &v1alpha1.FirewallAddress{
		ObjectMeta: metav1.ObjectMeta{Namespace: "cam1", Name: name},
		Spec:       v1alpha1.FirewallAddressSpec{Type: "ipmask", Subnet: subnet, Comment: "camera"},
	}
}

func TestAddressLifecycle(t *testing.T) {
	fg := newFakeFG()
	c := newClient(t, address("cam1-server", "10.12.1.0/24"))
	r := NewAddressReconciler(c, opts(fg))

	res := reconcileOnce(t, r, "cam1", "cam1-server")
	if res.RequeueAfter != 5*time.Minute {
		t.Errorf("RequeueAfter = %v", res.RequeueAfter)
	}
	got := fg.get("firewall/address", "cam1-server")
	if got == nil {
		t.Fatal("address was not created")
	}
	if got["subnet"] != "10.12.1.0 255.255.255.0" || got["type"] != "ipmask" {
		t.Errorf("created %v", got)
	}
	if got["comment"] != "[k8s:cam1/cam1-server] camera" {
		t.Errorf("comment = %q", got["comment"])
	}
	var a v1alpha1.FirewallAddress
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "cam1", Name: "cam1-server"}, &a); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(a.Finalizers, Finalizer) {
		t.Error("finalizer not added")
	}
	if cond := readyOf(t, a.Status.Conditions); cond.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %+v", cond)
	}
	if a.Status.Name != "cam1-server" {
		t.Errorf("status.name = %q", a.Status.Name)
	}
	fg.takeWrites()

	// No change: nothing is written.
	reconcileOnce(t, r, "cam1", "cam1-server")
	if w := fg.takeWrites(); len(w) != 0 {
		t.Errorf("unexpected writes: %v", w)
	}

	// A change made on the FortiGate by hand is reverted, and only the changed field is sent.
	_ = fg.Update(context.Background(), "firewall/address", "cam1-server", fortigate.Object{"subnet": "10.99.0.0 255.255.0.0"})
	fg.takeWrites()
	reconcileOnce(t, r, "cam1", "cam1-server")
	if got := fg.get("firewall/address", "cam1-server"); got["subnet"] != "10.12.1.0 255.255.255.0" {
		t.Errorf("drift not repaired: %v", got["subnet"])
	}
	if w := fg.takeWrites(); len(w) != 1 {
		t.Errorf("writes = %v", w)
	}

	// Deleting the resource deletes the FortiGate object and releases the finalizer.
	if err := c.Delete(context.Background(), &a); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, "cam1", "cam1-server")
	if fg.get("firewall/address", "cam1-server") != nil {
		t.Error("address not deleted")
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "cam1", Name: "cam1-server"}, &a); err == nil {
		t.Error("resource still exists")
	}
}

func TestAddressConflictAndAdopt(t *testing.T) {
	fg := newFakeFG()
	fg.put("firewall/address", fortigate.Object{"name": "nas", "type": "ipmask", "subnet": "10.13.0.0 255.255.0.0", "comment": "made by hand"})
	a := address("nas", "10.13.0.0/16")
	c := newClient(t, a)
	r := NewAddressReconciler(c, opts(fg))

	reconcileOnce(t, r, "cam1", "nas")
	if w := fg.takeWrites(); len(w) != 0 {
		t.Errorf("wrote to an object it does not own: %v", w)
	}
	var got v1alpha1.FirewallAddress
	_ = c.Get(context.Background(), client.ObjectKeyFromObject(a), &got)
	if cond := readyOf(t, got.Status.Conditions); cond.Reason != v1alpha1.ReasonConflict {
		t.Errorf("Ready = %+v", cond)
	}

	// The adopt annotation takes it over.
	got.Annotations = map[string]string{v1alpha1.AdoptAnnotation: "true"}
	if err := c.Update(context.Background(), &got); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, "cam1", "nas")
	if o := fg.get("firewall/address", "nas"); o["comment"] != "[k8s:cam1/nas] camera" {
		t.Errorf("comment = %q", o["comment"])
	}

	// An object owned by another resource is never taken over, even with the annotation.
	fg.put("firewall/address", fortigate.Object{"name": "other", "type": "ipmask", "subnet": "10.1.0.0 255.255.0.0", "comment": "[k8s:live/other]"})
	b := address("other", "10.2.0.0/16")
	b.Annotations = map[string]string{v1alpha1.AdoptAnnotation: "true"}
	if err := c.Create(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	fg.takeWrites()
	reconcileOnce(t, r, "cam1", "other")
	if w := fg.takeWrites(); len(w) != 0 {
		t.Errorf("took over an owned object: %v", w)
	}
	// Deleting it leaves the other owner's object alone.
	_ = c.Get(context.Background(), client.ObjectKeyFromObject(b), b)
	_ = c.Delete(context.Background(), b)
	reconcileOnce(t, r, "cam1", "other")
	if fg.get("firewall/address", "other") == nil {
		t.Error("deleted an object owned by another resource")
	}
}

func TestAddressInvalidSubnet(t *testing.T) {
	fg := newFakeFG()
	c := newClient(t, address("bad", "10.12.1.5/16"))
	r := NewAddressReconciler(c, opts(fg))
	reconcileOnce(t, r, "cam1", "bad")
	var got v1alpha1.FirewallAddress
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: "cam1", Name: "bad"}, &got)
	cond := readyOf(t, got.Status.Conditions)
	if cond.Reason != v1alpha1.ReasonInvalid || !strings.Contains(cond.Message, "10.12.0.0/16") {
		t.Errorf("Ready = %+v", cond)
	}
	if w := fg.takeWrites(); len(w) != 0 {
		t.Errorf("writes = %v", w)
	}
}

func TestService(t *testing.T) {
	fg := newFakeFG()
	s := &v1alpha1.FirewallService{
		ObjectMeta: metav1.ObjectMeta{Namespace: "live", Name: "rtmp"},
		Spec:       v1alpha1.FirewallServiceSpec{CommonSpec: v1alpha1.CommonSpec{Name: "RTMP-k8s"}, TCPPortrange: []string{"1935", "8000-8100"}},
	}
	c := newClient(t, s)
	r := NewServiceReconciler(c, opts(fg))
	reconcileOnce(t, r, "live", "rtmp")
	got := fg.get("firewall.service/custom", "RTMP-k8s")
	if got == nil || got["tcp-portrange"] != "1935 8000-8100" || got["udp-portrange"] != "" {
		t.Fatalf("created %v", got)
	}
	fg.takeWrites()
	reconcileOnce(t, r, "live", "rtmp")
	if w := fg.takeWrites(); len(w) != 0 {
		t.Errorf("unexpected writes: %v", w)
	}
}

func policy(ns, name string) *v1alpha1.FirewallPolicy {
	return &v1alpha1.FirewallPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Generation: 1},
		Spec: v1alpha1.FirewallPolicySpec{
			SrcIntf: []string{"sw10"}, DstIntf: []string{"dmz"},
			SrcAddr: []string{"VLAN10 subnet"}, DstAddr: []string{"k8s-svc"},
			Service: []string{"HTTP", "HTTPS"},
		},
	}
}

func seedPolicies(fg *fakeFG) {
	for i, n := range []string{"al-mgmt", "al-sv", "deny-all"} {
		fg.put(policyPath, fortigate.Object{"policyid": i + 1, "name": n, "action": "accept"})
	}
}

func TestPolicyCreateAndPosition(t *testing.T) {
	fg := newFakeFG()
	seedPolicies(fg)
	p := policy("cam1", "al-cam1-k8s")
	p.Spec.Position = &v1alpha1.PolicyPosition{Before: "deny-all"}
	c := newClient(t, p)
	r := NewPolicyReconciler(c, opts(fg))

	reconcileOnce(t, r, "cam1", "al-cam1-k8s")
	if got := fg.names(policyPath); !slices.Equal(got, []string{"al-mgmt", "al-sv", "al-cam1-k8s", "deny-all"}) {
		t.Errorf("order = %v", got)
	}
	o := fg.get(policyPath, "4")
	if o == nil {
		t.Fatal("policy 4 not created")
	}
	for k, want := range map[string]string{"action": "accept", "nat": "disable", "status": "enable", "schedule": "always", "logtraffic": "utm", "comments": "[k8s:cam1/al-cam1-k8s]"} {
		if o[k] != want {
			t.Errorf("%s = %v, want %s", k, o[k], want)
		}
	}
	if names, _ := namesOf(o["service"]); !slices.Equal(names, []string{"HTTP", "HTTPS"}) {
		t.Errorf("service = %v", o["service"])
	}
	var got v1alpha1.FirewallPolicy
	_ = c.Get(context.Background(), client.ObjectKeyFromObject(p), &got)
	if got.Status.PolicyID != 4 {
		t.Errorf("status.policyID = %d", got.Status.PolicyID)
	}
	if cond := readyOf(t, got.Status.Conditions); cond.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %+v", cond)
	}
	fg.takeWrites()

	// Correct place and values: nothing is written. Reordered name lists are not a change.
	_ = fg.Update(context.Background(), policyPath, "4", fortigate.Object{"service": nameList([]string{"HTTPS", "HTTP"})})
	fg.takeWrites()
	reconcileOnce(t, r, "cam1", "al-cam1-k8s")
	if w := fg.takeWrites(); len(w) != 0 {
		t.Errorf("unexpected writes: %v", w)
	}

	// Moved below the reference by hand: moved back.
	_ = fg.Move(context.Background(), policyPath, "4", "after", "3")
	fg.takeWrites()
	reconcileOnce(t, r, "cam1", "al-cam1-k8s")
	if got := fg.names(policyPath); !slices.Equal(got, []string{"al-mgmt", "al-sv", "al-cam1-k8s", "deny-all"}) {
		t.Errorf("order after repair = %v", got)
	}

	// Deleted with the resource.
	_ = c.Get(context.Background(), client.ObjectKeyFromObject(p), &got)
	_ = c.Delete(context.Background(), &got)
	reconcileOnce(t, r, "cam1", "al-cam1-k8s")
	if fg.get(policyPath, "4") != nil {
		t.Error("policy not deleted")
	}
}

func TestPolicyConflictBetweenNamespaces(t *testing.T) {
	fg := newFakeFG()
	fg.put(policyPath, fortigate.Object{"policyid": 7, "name": "al-web", "comments": "[k8s:live/al-web]"})
	p := policy("cam1", "al-web")
	c := newClient(t, p)
	r := NewPolicyReconciler(c, opts(fg))
	reconcileOnce(t, r, "cam1", "al-web")
	var got v1alpha1.FirewallPolicy
	_ = c.Get(context.Background(), client.ObjectKeyFromObject(p), &got)
	cond := readyOf(t, got.Status.Conditions)
	if cond.Reason != v1alpha1.ReasonConflict || !strings.Contains(cond.Message, "live/al-web") {
		t.Errorf("Ready = %+v", cond)
	}
	if w := fg.takeWrites(); len(w) != 0 {
		t.Errorf("writes = %v", w)
	}
}

func TestPolicyOrphan(t *testing.T) {
	fg := newFakeFG()
	p := policy("cam1", "al-keep")
	p.Spec.DeletionPolicy = v1alpha1.DeletionPolicyOrphan
	c := newClient(t, p)
	r := NewPolicyReconciler(c, opts(fg))
	reconcileOnce(t, r, "cam1", "al-keep")
	var got v1alpha1.FirewallPolicy
	_ = c.Get(context.Background(), client.ObjectKeyFromObject(p), &got)
	_ = c.Delete(context.Background(), &got)
	reconcileOnce(t, r, "cam1", "al-keep")
	if fg.names(policyPath)[0] != "al-keep" {
		t.Error("orphaned policy was deleted")
	}
}

func TestPolicyDryRun(t *testing.T) {
	fg := newFakeFG()
	seedPolicies(fg)
	p := policy("cam1", "al-new")
	c := newClient(t, p)
	o := opts(fg)
	o.DryRun = true
	r := NewPolicyReconciler(c, o)
	reconcileOnce(t, r, "cam1", "al-new")
	if w := fg.takeWrites(); len(w) != 0 {
		t.Errorf("dry run wrote: %v", w)
	}
	var got v1alpha1.FirewallPolicy
	_ = c.Get(context.Background(), client.ObjectKeyFromObject(p), &got)
	if cond := readyOf(t, got.Status.Conditions); cond.Reason != v1alpha1.ReasonDryRun {
		t.Errorf("Ready = %+v", cond)
	}
}

func TestPolicyNameTooLong(t *testing.T) {
	fg := newFakeFG()
	p := policy("cam1", "x")
	p.Spec.Name = strings.Repeat("a", 36)
	c := newClient(t, p)
	reconcileOnce(t, NewPolicyReconciler(c, opts(fg)), "cam1", "x")
	var got v1alpha1.FirewallPolicy
	_ = c.Get(context.Background(), client.ObjectKeyFromObject(p), &got)
	if cond := readyOf(t, got.Status.Conditions); cond.Reason != v1alpha1.ReasonInvalid {
		t.Errorf("Ready = %+v", cond)
	}
}

func TestOwnerOf(t *testing.T) {
	for in, want := range map[string]string{
		"[k8s:cam1/al-web] note": "cam1/al-web",
		"[k8s:cam1/al-web]":      "cam1/al-web",
		"note [k8s:cam1/al-web]": "",
		"":                       "",
	} {
		if got := ownerOf(in); got != want {
			t.Errorf("ownerOf(%q) = %q, want %q", in, got, want)
		}
	}
}
