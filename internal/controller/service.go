package controller

import (
	"fmt"
	"regexp"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/TechnoTUT/fortigate-operator/api/v1alpha1"
	"github.com/TechnoTUT/fortigate-operator/internal/fortigate"
)

// NewServiceReconciler returns the reconciler for FirewallService.
func NewServiceReconciler(c client.Client, o Options) *NamedReconciler[*v1alpha1.FirewallService] {
	return &NamedReconciler[*v1alpha1.FirewallService]{Client: c, Options: o, k: namedKind[*v1alpha1.FirewallService]{
		kind:         "FirewallService",
		path:         "firewall.service/custom",
		commentField: "comment",
		maxNameLen:   79,
		newObject:    func() *v1alpha1.FirewallService { return &v1alpha1.FirewallService{} },
		common:       func(o *v1alpha1.FirewallService) v1alpha1.CommonSpec { return o.Spec.CommonSpec },
		status:       func(o *v1alpha1.FirewallService) *v1alpha1.ObjectStatus { return &o.Status },
		comment:      func(o *v1alpha1.FirewallService) string { return o.Spec.Comment },
		desired:      serviceFields,
	}}
}

// dst[-dst][:src[-src]]
var portRangeRE = regexp.MustCompile(`^\d{1,5}(-\d{1,5})?(:\d{1,5}(-\d{1,5})?)?$`)

func serviceFields(o *v1alpha1.FirewallService) (fortigate.Object, error) {
	s := o.Spec
	if len(s.TCPPortrange)+len(s.UDPPortrange)+len(s.SCTPPortrange) == 0 {
		return nil, fmt.Errorf("set at least one of tcpPortrange, udpPortrange or sctpPortrange")
	}
	// protocol is left out: a new custom service already uses the TCP/UDP/SCTP family, and its
	// value differs between versions (TCP/UDP/SCTP on 6.2, TCP/UDP/UDP-Lite/SCTP on 7.4 and later).
	f := fortigate.Object{
		"category": s.Category,
	}
	for field, ranges := range map[string][]string{
		"tcp-portrange":  s.TCPPortrange,
		"udp-portrange":  s.UDPPortrange,
		"sctp-portrange": s.SCTPPortrange,
	} {
		for _, pr := range ranges {
			if !portRangeRE.MatchString(pr) {
				return nil, fmt.Errorf("%s: %q is not a port range such as 1935, 8000-8100 or 53:1024-65535", field, pr)
			}
		}
		f[field] = strings.Join(ranges, " ")
	}
	return f, nil
}
