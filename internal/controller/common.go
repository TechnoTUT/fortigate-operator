// Package controller reconciles the fortigate.technotut.net resources against a FortiGate.
package controller

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/TechnoTUT/fortigate-operator/api/v1alpha1"
	"github.com/TechnoTUT/fortigate-operator/internal/fortigate"
)

// +kubebuilder:rbac:groups=fortigate.technotut.net,resources=firewallpolicies;firewalladdresses;firewallservices,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=fortigate.technotut.net,resources=firewallpolicies/status;firewalladdresses/status;firewallservices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=fortigate.technotut.net,resources=firewallpolicies/finalizers;firewalladdresses/finalizers;firewallservices/finalizers,verbs=update

// Finalizer keeps a resource until its FortiGate object has been removed.
const Finalizer = "fortigate.technotut.net/finalizer"

// API is the part of the FortiGate client the reconcilers use.
type API interface {
	List(ctx context.Context, path string) ([]fortigate.Object, error)
	Get(ctx context.Context, path, mkey string) (fortigate.Object, error)
	Create(ctx context.Context, path string, body fortigate.Object) (string, error)
	Update(ctx context.Context, path, mkey string, body fortigate.Object) error
	Delete(ctx context.Context, path, mkey string) error
	Move(ctx context.Context, path, mkey, where, ref string) error
}

// Options are shared by every reconciler.
type Options struct {
	FortiGate API
	// DryRun logs the changes instead of writing them.
	DryRun bool
	// Resync is how often a resource is checked against the FortiGate when nothing changed in Kubernetes,
	// which repairs changes made on the FortiGate by hand.
	Resync time.Duration
}

// --- ownership marker ---

// The marker at the start of the comment field records which resource owns a FortiGate object.
// It survives a rebuild of the cluster, unlike anything kept in the resource status.
var markerRE = regexp.MustCompile(`^\[k8s:([^\]/\s]+)/([^\]/\s]+)\]\s?`)

func marker(obj client.Object) string {
	return fmt.Sprintf("[k8s:%s/%s]", obj.GetNamespace(), obj.GetName())
}

// withMarker returns the comment to write for obj.
func withMarker(obj client.Object, comment string) string {
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return marker(obj)
	}
	return marker(obj) + " " + comment
}

// ownerOf returns "namespace/name" from the marker in a comment, or "" when there is no marker.
func ownerOf(comment string) string {
	m := markerRE.FindStringSubmatch(comment)
	if m == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}

// ownership checks whether obj may manage an existing FortiGate object with the given comment.
// It returns "" when it may, or the reason why it may not. The adopt annotation takes over
// objects without a marker only; an object owned by another resource is never taken over.
func ownership(obj client.Object, comment string) string {
	owner := ownerOf(comment)
	self := obj.GetNamespace() + "/" + obj.GetName()
	switch {
	case owner == self:
		return ""
	case owner == "" && obj.GetAnnotations()[v1alpha1.AdoptAnnotation] == "true":
		return ""
	case owner == "":
		return fmt.Sprintf("an object with this name already exists on the FortiGate and was not created by this controller; set the annotation %s: \"true\" to take it over", v1alpha1.AdoptAnnotation)
	default:
		return fmt.Sprintf("the FortiGate object belongs to %s", owner)
	}
}

// --- field comparison ---

// nameList converts names to the FortiOS form of a multi-value reference field.
func nameList(names []string) []any {
	out := make([]any, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]any{"name": n})
	}
	return out
}

// namesOf extracts the names from a multi-value reference field as returned by the API.
func namesOf(v any) ([]string, bool) {
	list, ok := v.([]any)
	if !ok {
		return nil, v == nil
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, false
		}
		n, _ := m["name"].(string)
		out = append(out, n)
	}
	return out, true
}

func scalarString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprint(t)
	}
}

// diff returns the keys of want whose values differ from have, sorted.
// Name lists are compared as sets; the FortiGate does not care about their order.
func diff(want, have fortigate.Object) []string {
	var out []string
	for k, w := range want {
		h := have[k]
		switch wv := w.(type) {
		case []any:
			wn, _ := namesOf(wv)
			hn, ok := namesOf(h)
			if !ok || !sameSet(wn, hn) {
				out = append(out, k)
			}
		default:
			if normSpace(scalarString(w)) != normSpace(scalarString(h)) {
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func normSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// --- status ---

func setReady(conds *[]metav1.Condition, gen int64, status metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(conds, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: gen,
	})
}

// specChanged lets through the events that need a reconcile: spec changes, annotation changes
// (for the adopt annotation), deletion, and creation. Status-only updates are ignored, so that
// writing the status does not trigger another round of API calls.
func specChanged() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectOld == nil || e.ObjectNew == nil {
				return true
			}
			if e.ObjectNew.GetDeletionTimestamp() != nil {
				return true
			}
			if e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration() {
				return true
			}
			return !mapsEqual(e.ObjectOld.GetAnnotations(), e.ObjectNew.GetAnnotations())
		},
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

func fortiName(obj client.Object, spec v1alpha1.CommonSpec) string {
	if spec.Name != "" {
		return spec.Name
	}
	return obj.GetName()
}

func now() *metav1.Time { t := metav1.Now(); return &t }
