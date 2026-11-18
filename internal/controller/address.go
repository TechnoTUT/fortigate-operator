package controller

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/TechnoTUT/fortigate-operator/api/v1alpha1"
	"github.com/TechnoTUT/fortigate-operator/internal/fortigate"
)

// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch

// serviceRefIndex indexes FirewallAddress by spec.serviceRef.name.
const serviceRefIndex = "spec.serviceRef.name"

// NewAddressReconciler returns the reconciler for FirewallAddress.
func NewAddressReconciler(c client.Client, o Options) *NamedReconciler[*v1alpha1.FirewallAddress] {
	return &NamedReconciler[*v1alpha1.FirewallAddress]{Client: c, Options: o, k: namedKind[*v1alpha1.FirewallAddress]{
		kind:         "FirewallAddress",
		path:         "firewall/address",
		commentField: "comment",
		maxNameLen:   79,
		newObject:    func() *v1alpha1.FirewallAddress { return &v1alpha1.FirewallAddress{} },
		common:       func(o *v1alpha1.FirewallAddress) v1alpha1.CommonSpec { return o.Spec.CommonSpec },
		status:       func(o *v1alpha1.FirewallAddress) *v1alpha1.ObjectStatus { return &o.Status.ObjectStatus },
		comment:      func(o *v1alpha1.FirewallAddress) string { return o.Spec.Comment },
		desired:      addressFields,
		setup:        watchServices,
	}}
}

func addressFields(ctx context.Context, c client.Reader, o *v1alpha1.FirewallAddress) (fortigate.Object, error) {
	s := o.Spec
	typ := s.Type
	if typ == "" {
		typ = "ipmask"
	}
	if s.ServiceRef != nil && typ != "ipmask" {
		return nil, fmt.Errorf("serviceRef can only be used with type ipmask")
	}
	f := fortigate.Object{
		"type":                 typ,
		"associated-interface": s.AssociatedInterface,
	}
	switch typ {
	case "ipmask":
		cidr := s.Subnet
		if s.ServiceRef != nil {
			if s.Subnet != "" {
				return nil, fmt.Errorf("set only one of subnet or serviceRef")
			}
			ip, err := serviceAddress(ctx, c, o.Namespace, s.ServiceRef.Name)
			if err != nil {
				return nil, err
			}
			cidr = ip.String() + "/32"
		}
		subnet, err := fortiSubnet(cidr)
		if err != nil {
			return nil, err
		}
		f["subnet"] = subnet
		o.Status.Address = cidr
	case "iprange":
		start, err1 := netip.ParseAddr(s.StartIP)
		end, err2 := netip.ParseAddr(s.EndIP)
		if err1 != nil || err2 != nil || !start.Is4() || !end.Is4() {
			return nil, fmt.Errorf("startIP and endIP must be IPv4 addresses")
		}
		if end.Less(start) {
			return nil, fmt.Errorf("endIP is lower than startIP")
		}
		f["start-ip"] = start.String()
		f["end-ip"] = end.String()
		o.Status.Address = ""
	case "fqdn":
		if strings.TrimSpace(s.FQDN) == "" {
			return nil, fmt.Errorf("fqdn is required for type fqdn")
		}
		f["fqdn"] = s.FQDN
		o.Status.Address = ""
	default:
		return nil, fmt.Errorf("unsupported type %q", typ)
	}
	return f, nil
}

// serviceAddress returns the IPv4 load balancer address of a Service. It returns a
// *referenceError while the Service is missing or has no address yet.
func serviceAddress(ctx context.Context, c client.Reader, namespace, name string) (netip.Addr, error) {
	var svc corev1.Service
	err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &svc)
	if apierrors.IsNotFound(err) || (err == nil && !svc.DeletionTimestamp.IsZero()) {
		return netip.Addr{}, &referenceError{fmt.Sprintf("Service %s/%s does not exist", namespace, name)}
	}
	if err != nil {
		return netip.Addr{}, err
	}
	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		return netip.Addr{}, &referenceError{fmt.Sprintf("Service %s/%s is of type %s, not LoadBalancer", namespace, name, svc.Spec.Type)}
	}
	var ips []netip.Addr
	for _, ing := range svc.Status.LoadBalancer.Ingress {
		ip, err := netip.ParseAddr(ing.IP)
		if err != nil || !ip.Is4() || slices.Contains(ips, ip) {
			continue
		}
		ips = append(ips, ip)
	}
	switch len(ips) {
	case 0:
		return netip.Addr{}, &referenceError{fmt.Sprintf("Service %s/%s has no IPv4 load balancer address yet", namespace, name)}
	case 1:
		return ips[0], nil
	default:
		return netip.Addr{}, fmt.Errorf("Service %s/%s has %d IPv4 load balancer addresses %v; a FirewallAddress holds one", namespace, name, len(ips), ips)
	}
}

// watchServices reconciles the FirewallAddresses that refer to a Service when the Service is
// created or deleted, or when its type or load balancer addresses change.
func watchServices(mgr ctrl.Manager, b *builder.Builder) error {
	err := mgr.GetFieldIndexer().IndexField(context.Background(), &v1alpha1.FirewallAddress{}, serviceRefIndex, serviceRefKey)
	if err != nil {
		return err
	}
	b.Watches(&corev1.Service{},
		handler.EnqueueRequestsFromMapFunc(addressesForService(mgr.GetClient())),
		builder.WithPredicates(serviceAddressChanged()))
	return nil
}

func serviceRefKey(o client.Object) []string {
	a, ok := o.(*v1alpha1.FirewallAddress)
	if !ok || a.Spec.ServiceRef == nil {
		return nil
	}
	return []string{a.Spec.ServiceRef.Name}
}

func addressesForService(c client.Reader) handler.MapFunc {
	return func(ctx context.Context, svc client.Object) []reconcile.Request {
		var list v1alpha1.FirewallAddressList
		if err := c.List(ctx, &list, client.InNamespace(svc.GetNamespace()), client.MatchingFields{serviceRefIndex: svc.GetName()}); err != nil {
			log.FromContext(ctx).Error(err, "cannot list FirewallAddresses for a Service", "service", client.ObjectKeyFromObject(svc))
			return nil
		}
		out := make([]reconcile.Request, 0, len(list.Items))
		for _, a := range list.Items {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&a)})
		}
		return out
	}
}

func serviceAddressChanged() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			o, ok1 := e.ObjectOld.(*corev1.Service)
			n, ok2 := e.ObjectNew.(*corev1.Service)
			if !ok1 || !ok2 {
				return true
			}
			return o.Spec.Type != n.Spec.Type ||
				o.DeletionTimestamp.IsZero() != n.DeletionTimestamp.IsZero() ||
				!equality.Semantic.DeepEqual(o.Status.LoadBalancer, n.Status.LoadBalancer)
		},
	}
}

// fortiSubnet converts CIDR notation to the "address mask" form the FortiGate stores.
func fortiSubnet(cidr string) (string, error) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil || !p.Addr().Is4() {
		return "", fmt.Errorf("subnet %q is not an IPv4 CIDR such as 10.12.0.0/16", cidr)
	}
	if p.Masked() != p {
		return "", fmt.Errorf("subnet %q has host bits set; use %s", cidr, p.Masked())
	}
	mask := net.CIDRMask(p.Bits(), 32)
	return fmt.Sprintf("%s %d.%d.%d.%d", p.Addr(), mask[0], mask[1], mask[2], mask[3]), nil
}
