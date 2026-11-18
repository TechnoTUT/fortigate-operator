package controller

import (
	"fmt"
	"net"
	"net/netip"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/TechnoTUT/fortigate-operator/api/v1alpha1"
	"github.com/TechnoTUT/fortigate-operator/internal/fortigate"
)

// NewAddressReconciler returns the reconciler for FirewallAddress.
func NewAddressReconciler(c client.Client, o Options) *NamedReconciler[*v1alpha1.FirewallAddress] {
	return &NamedReconciler[*v1alpha1.FirewallAddress]{Client: c, Options: o, k: namedKind[*v1alpha1.FirewallAddress]{
		kind:         "FirewallAddress",
		path:         "firewall/address",
		commentField: "comment",
		maxNameLen:   79,
		newObject:    func() *v1alpha1.FirewallAddress { return &v1alpha1.FirewallAddress{} },
		common:       func(o *v1alpha1.FirewallAddress) v1alpha1.CommonSpec { return o.Spec.CommonSpec },
		status:       func(o *v1alpha1.FirewallAddress) *v1alpha1.ObjectStatus { return &o.Status },
		comment:      func(o *v1alpha1.FirewallAddress) string { return o.Spec.Comment },
		desired:      addressFields,
	}}
}

func addressFields(o *v1alpha1.FirewallAddress) (fortigate.Object, error) {
	s := o.Spec
	typ := s.Type
	if typ == "" {
		typ = "ipmask"
	}
	f := fortigate.Object{
		"type":                 typ,
		"associated-interface": s.AssociatedInterface,
	}
	switch typ {
	case "ipmask":
		subnet, err := fortiSubnet(s.Subnet)
		if err != nil {
			return nil, err
		}
		f["subnet"] = subnet
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
	case "fqdn":
		if strings.TrimSpace(s.FQDN) == "" {
			return nil, fmt.Errorf("fqdn is required for type fqdn")
		}
		f["fqdn"] = s.FQDN
	default:
		return nil, fmt.Errorf("unsupported type %q", typ)
	}
	return f, nil
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
