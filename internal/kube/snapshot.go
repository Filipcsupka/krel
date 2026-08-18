package kube

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/filipcsupka/krel/internal/graph"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
)

type Options struct {
	Namespace   string
	Kubeconfig  string
	ContextName string
}

type Snapshot struct {
	Context    string
	Namespace  string
	Namespaces []string
	Contexts   []string
	Options    Options
	Graph      *graph.Graph
	LoadErrors []string
	PodMetrics map[string]PodMetric
}

// PodMetric is the current metrics-server usage for a pod, summed across
// its containers. Zero-value (found=false) means metrics-server isn't
// installed/reachable or the pod hasn't reported usage yet.
type PodMetric struct {
	CPUMilli int64
	MemBytes int64
	Found    bool
}

type resourceDef struct {
	gvr        schema.GroupVersionResource
	kind       string
	namespaced bool
}

var phaseOneResources = []resourceDef{
	{schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}, "Pod", true},
	{schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"}, "Service", true},
	{schema.GroupVersionResource{Group: "", Version: "v1", Resource: "endpoints"}, "Endpoints", true},
	{schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}, "ConfigMap", true},
	{schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}, "Secret", true},
	{schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}, "PersistentVolumeClaim", true},
	{schema.GroupVersionResource{Group: "", Version: "v1", Resource: "serviceaccounts"}, "ServiceAccount", true},
	{schema.GroupVersionResource{Group: "", Version: "v1", Resource: "resourcequotas"}, "ResourceQuota", true},
	{schema.GroupVersionResource{Group: "", Version: "v1", Resource: "limitranges"}, "LimitRange", true},
	{schema.GroupVersionResource{Group: "", Version: "v1", Resource: "events"}, "Event", true},
	{schema.GroupVersionResource{Group: "", Version: "v1", Resource: "nodes"}, "Node", false},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, "Deployment", true},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}, "ReplicaSet", true},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}, "StatefulSet", true},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}, "DaemonSet", true},
	{schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, "Job", true},
	{schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}, "CronJob", true},
	{schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}, "EndpointSlice", true},
	{schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}, "Ingress", true},
	{schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}, "NetworkPolicy", true},
	{schema.GroupVersionResource{Group: "policy", Version: "v1", Resource: "poddisruptionbudgets"}, "PodDisruptionBudget", true},
	{schema.GroupVersionResource{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}, "HorizontalPodAutoscaler", true},
	{schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}, "Certificate", true},
	{schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "issuers"}, "Issuer", true},
	{schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers"}, "ClusterIssuer", false},
	{schema.GroupVersionResource{Group: "route.openshift.io", Version: "v1", Resource: "routes"}, "Route", true},
	{schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "subscriptions"}, "Subscription", true},
	{schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "installplans"}, "InstallPlan", true},
	{schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "clusterserviceversions"}, "ClusterServiceVersion", true},
}

// optionalResourceKinds are fetched best-effort: their CRDs don't exist on
// every cluster (OLM's operators.coreos.com group is OpenShift/OLM-only), so
// a "the server could not find the requested resource" error just means
// this cluster doesn't have that CRD installed — not worth surfacing as a
// load error on every snapshot for every non-OLM user. A real error against
// one of these kinds (e.g. RBAC denial) still gets recorded.
var optionalResourceKinds = map[string]bool{
	"Subscription":          true,
	"InstallPlan":           true,
	"ClusterServiceVersion": true,
}

// isBenignMissingResource reports whether err represents an optional kind's
// CRD simply not being installed on this cluster, as opposed to a real
// failure (RBAC denial, network error, ...) worth surfacing.
func isBenignMissingResource(kind string, err error) bool {
	if !optionalResourceKinds[kind] {
		return false
	}
	// apierrors.IsNotFound catches the well-formed case; the dynamic client
	// talking to a completely unregistered GVR sometimes surfaces a
	// StatusError whose Reason doesn't round-trip cleanly, so fall back to
	// matching the apiserver's standard "no route for this resource"
	// message rather than risk swallowing a real error.
	return apierrors.IsNotFound(err) || strings.Contains(err.Error(), "could not find the requested resource")
}

func LoadSnapshot(ctx context.Context, opts Options) (Snapshot, error) {
	opts.Kubeconfig = ResolveKubeconfig(opts.Kubeconfig)
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.Kubeconfig != "" {
		loadingRules.ExplicitPath = opts.Kubeconfig
	}

	overrides := &clientcmd.ConfigOverrides{CurrentContext: opts.ContextName}
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)

	rawConfig, err := clientConfig.RawConfig()
	if err != nil {
		return Snapshot{}, err
	}
	contextName := rawConfig.CurrentContext
	if opts.ContextName != "" {
		contextName = opts.ContextName
	}
	contexts := contextNames(rawConfig)
	namespace := opts.Namespace
	if namespace == "" {
		namespace = namespaceFromContext(rawConfig, contextName)
	}
	if namespace == "" {
		namespace = "default"
	}

	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return Snapshot{}, err
	}
	restConfig.WarningHandler = rest.NoWarnings{}
	dyn, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return Snapshot{}, err
	}

	namespaces := loadNamespaces(ctx, dyn)
	var objects []graph.Object
	var loadErrors []string
	for _, def := range phaseOneResources {
		var list *unstructured.UnstructuredList
		var err error
		if def.namespaced {
			list, err = dyn.Resource(def.gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
		} else {
			list, err = dyn.Resource(def.gvr).List(ctx, metav1.ListOptions{})
		}
		if err != nil {
			if !isBenignMissingResource(def.kind, err) {
				loadErrors = append(loadErrors, fmt.Sprintf("%s: %v", def.kind, err))
			}
			continue
		}
		for i := range list.Items {
			item := list.Items[i]
			objects = append(objects, objectFromUnstructured(def.kind, &item))
		}
	}
	events := loadEvents(ctx, dyn, namespace)
	attachEvents(objects, events)

	if len(objects) == 0 && len(loadErrors) > 0 {
		return Snapshot{}, errors.New(strings.Join(loadErrors, "\n"))
	}

	return Snapshot{
		Context:    contextName,
		Namespace:  namespace,
		Namespaces: namespaces,
		Contexts:   contexts,
		Options:    Options{Namespace: namespace, Kubeconfig: opts.Kubeconfig, ContextName: contextName},
		Graph:      graph.Build(objects),
		LoadErrors: loadErrors,
		PodMetrics: loadPodMetrics(ctx, dyn, namespace),
	}, nil
}

var podMetricsGVR = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}

// loadPodMetrics is best-effort: clusters without metrics-server (or without
// RBAC for it) simply get an empty map, never an error.
func loadPodMetrics(ctx context.Context, dyn dynamic.Interface, namespace string) map[string]PodMetric {
	metrics := map[string]PodMetric{}
	list, err := dyn.Resource(podMetricsGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return metrics
	}
	for _, item := range list.Items {
		name := item.GetName()
		containers, _, _ := unstructured.NestedSlice(item.Object, "containers")
		var cpu, mem int64
		for _, c := range containers {
			container, ok := c.(map[string]any)
			if !ok {
				continue
			}
			usage, _, _ := unstructured.NestedMap(container, "usage")
			if cpuStr, ok := usage["cpu"].(string); ok {
				cpu += parseCPUMilli(cpuStr)
			}
			if memStr, ok := usage["memory"].(string); ok {
				mem += parseMemBytes(memStr)
			}
		}
		metrics[name] = PodMetric{CPUMilli: cpu, MemBytes: mem, Found: true}
	}
	return metrics
}

func parseCPUMilli(s string) int64 {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0
	}
	return q.MilliValue()
}

func parseMemBytes(s string) int64 {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0
	}
	return q.Value()
}

func ResolveKubeconfig(path string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	if strings.ContainsRune(path, os.PathSeparator) || filepath.IsAbs(path) {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	candidate := filepath.Join(home, ".kube", path)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return path
}

func contextNames(config api.Config) []string {
	contexts := make([]string, 0, len(config.Contexts))
	for name := range config.Contexts {
		contexts = append(contexts, name)
	}
	sort.Strings(contexts)
	return contexts
}

func namespaceFromContext(config api.Config, contextName string) string {
	ctx, ok := config.Contexts[contextName]
	if !ok {
		return ""
	}
	return ctx.Namespace
}

func objectFromUnstructured(kind string, item *unstructured.Unstructured) graph.Object {
	item.SetKind(kind)
	ref := graph.ObjectRef{
		Kind:      kind,
		Namespace: item.GetNamespace(),
		Name:      item.GetName(),
		UID:       types.UID(item.GetUID()),
	}
	return graph.Object{
		Ref:    ref,
		GVK:    item.GroupVersionKind().String(),
		Labels: item.GetLabels(),
		Raw:    item,
	}
}

func loadNamespaces(ctx context.Context, dyn dynamic.Interface) []string {
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	list, err := dyn.Resource(gvr).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	namespaces := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		namespaces = append(namespaces, item.GetName())
	}
	sort.Strings(namespaces)
	return namespaces
}

func loadEvents(ctx context.Context, dyn dynamic.Interface, namespace string) map[string][]string {
	events := map[string][]string{}
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "events"}
	list, err := dyn.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return events
	}
	for _, event := range list.Items {
		kind, _, _ := unstructured.NestedString(event.Object, "involvedObject", "kind")
		name, _, _ := unstructured.NestedString(event.Object, "involvedObject", "name")
		message, _, _ := unstructured.NestedString(event.Object, "message")
		reason, _, _ := unstructured.NestedString(event.Object, "reason")
		eventType, _, _ := unstructured.NestedString(event.Object, "type")
		if kind == "" || name == "" || message == "" {
			continue
		}
		if eventType == "" {
			eventType = "Normal"
		}
		events[kind+"/"+namespace+"/"+name] = append(events[kind+"/"+namespace+"/"+name], eventType+" "+reason+": "+message)
	}
	for key := range events {
		sort.Strings(events[key])
		if len(events[key]) > 20 {
			events[key] = events[key][len(events[key])-20:]
		}
	}
	return events
}

func attachEvents(objects []graph.Object, events map[string][]string) {
	for i := range objects {
		objects[i].Events = events[objects[i].Ref.Key()]
	}
}
