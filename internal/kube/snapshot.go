package kube

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/filipcsupka/krel/internal/graph"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
)

type Options struct {
	Namespace     string
	Kubeconfig    string
	ContextName   string
	AllNamespaces bool
	// ResourceKind asks the snapshot loader to include one discovered kind
	// outside the default relationship profile. This keeps startup fast on
	// CRD-heavy clusters while making every listable kind browseable.
	ResourceKind string
}

type Snapshot struct {
	Context         string
	Namespace       string
	Namespaces      []string
	Contexts        []string
	Options         Options
	Graph           *graph.Graph
	LoadErrors      []string
	PodMetrics      map[string]PodMetric
	Resources       []ResourceType
	LoadedKinds     map[string]bool
	LoadedResources map[string]bool
}

// ResourceType is one preferred, listable API resource discovered from the
// active cluster. It powers k9s-style dynamic resource commands instead of
// limiting the UI to a compiled-in kind list.
type ResourceType struct {
	GVR        schema.GroupVersionResource
	Kind       string
	Namespaced bool
	ShortNames []string
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
	{schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}, "Application", true},
}

// relationshipProfile extends the core phase-one set with the operational
// APIs commonly present in the pft and asp inventories. Discovery supplies
// the preferred version and silently omits APIs absent from a cluster.
var relationshipProfile = map[string]bool{
	"/persistentvolumes":                                   true,
	"storage.k8s.io/storageclasses":                        true,
	"rbac.authorization.k8s.io/roles":                      true,
	"rbac.authorization.k8s.io/rolebindings":               true,
	"rbac.authorization.k8s.io/clusterroles":               true,
	"snapshot.storage.k8s.io/volumesnapshots":              true,
	"snapshot.storage.k8s.io/volumesnapshotclasses":        true,
	"snapshot.storage.k8s.io/volumesnapshotcontents":       true,
	"gateway.networking.k8s.io/gateways":                   true,
	"gateway.networking.k8s.io/gatewayclasses":             true,
	"gateway.networking.k8s.io/httproutes":                 true,
	"gateway.networking.k8s.io/grpcroutes":                 true,
	"gateway.networking.k8s.io/referencegrants":            true,
	"monitoring.coreos.com/servicemonitors":                true,
	"monitoring.coreos.com/podmonitors":                    true,
	"monitoring.coreos.com/prometheusrules":                true,
	"external-secrets.io/externalsecrets":                  true,
	"external-secrets.io/secretstores":                     true,
	"external-secrets.io/clustersecretstores":              true,
	"keda.sh/scaledobjects":                                true,
	"keda.sh/triggerauthentications":                       true,
	"argoproj.io/applicationsets":                          true,
	"argoproj.io/rollouts":                                 true,
	"argoproj.io/workflows":                                true,
	"argoproj.io/cronworkflows":                            true,
	"velero.io/backups":                                    true,
	"velero.io/restores":                                   true,
	"velero.io/schedules":                                  true,
	"oadp.openshift.io/dataprotectionapplications":         true,
	"build.openshift.io/buildconfigs":                      true,
	"apps.openshift.io/deploymentconfigs":                  true,
	"image.openshift.io/imagestreams":                      true,
	"operators.coreos.com/operatorgroups":                  true,
	"operators.coreos.com/catalogsources":                  true,
	"kafka.strimzi.io/kafkas":                              true,
	"kafka.strimzi.io/kafkatopics":                         true,
	"kafka.strimzi.io/kafkausers":                          true,
	"kafka.strimzi.io/kafkaconnects":                       true,
	"kafka.strimzi.io/kafkaconnectors":                     true,
	"kyverno.io/policies":                                  true,
	"wgpolicyk8s.io/policyreports":                         true,
	"/namespaces":                                          true,
	"config.openshift.io/clusteroperators":                 true,
	"config.openshift.io/clusterversions":                  true,
	"machineconfiguration.openshift.io/machineconfigpools": true,
	"project.openshift.io/projects":                        true,
	"cluster.open-cluster-management.io/managedclusters":   true,
}

// optionalResourceKinds are fetched best-effort: their CRDs don't exist on
// every cluster (OLM's operators.coreos.com group is OpenShift/OLM-only,
// ArgoCD's argoproj.io group only exists where ArgoCD is installed), so a
// "the server could not find the requested resource" error just means this
// cluster doesn't have that CRD installed — not worth surfacing as a load
// error on every snapshot for every user without it. A real error against
// one of these kinds (e.g. RBAC denial) still gets recorded.
var optionalResourceKinds = map[string]bool{
	"Subscription":          true,
	"InstallPlan":           true,
	"ClusterServiceVersion": true,
	"Application":           true,
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
	if namespace == "" && !opts.AllNamespaces {
		namespace = namespaceFromContext(rawConfig, contextName)
	}
	if namespace == "" && !opts.AllNamespaces {
		namespace = "default"
	}
	queryNamespace := namespace
	displayNamespace := namespace
	if opts.AllNamespaces {
		queryNamespace = ""
		displayNamespace = "all"
	}

	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return Snapshot{}, err
	}
	restConfig.WarningHandler = rest.NoWarnings{}
	// The relationship profile performs a short bounded burst of independent
	// list calls. client-go's default 5 QPS limiter serializes that burst and
	// makes CRD-rich clusters feel frozen; these limits match the 16-worker
	// loader while remaining modest for an interactive read-only client.
	restConfig.QPS = 30
	restConfig.Burst = 60
	dyn, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return Snapshot{}, err
	}

	resources := discoverResourceTypes(restConfig)
	namespaces := loadNamespaces(ctx, dyn)
	defs := snapshotResourceDefs(resources, opts.ResourceKind, opts.AllNamespaces)
	objects, loadErrors := loadResources(ctx, dyn, queryNamespace, defs)
	events := map[string][]string{}
	if !opts.AllNamespaces {
		events = loadEvents(ctx, dyn, queryNamespace)
	}
	attachEvents(objects, events)

	if len(objects) == 0 && len(loadErrors) > 0 {
		return Snapshot{}, errors.New(strings.Join(loadErrors, "\n"))
	}

	return Snapshot{
		Context:         contextName,
		Namespace:       displayNamespace,
		Namespaces:      namespaces,
		Contexts:        contexts,
		Options:         Options{Namespace: namespace, Kubeconfig: opts.Kubeconfig, ContextName: contextName, AllNamespaces: opts.AllNamespaces, ResourceKind: opts.ResourceKind},
		Graph:           graph.Build(objects),
		LoadErrors:      loadErrors,
		PodMetrics:      loadPodMetrics(ctx, dyn, queryNamespace),
		Resources:       resources,
		LoadedKinds:     loadedKinds(defs),
		LoadedResources: loadedResources(defs),
	}, nil
}

func loadedResources(defs []resourceDef) map[string]bool {
	out := map[string]bool{}
	for _, def := range defs {
		out[def.gvr.Group+"/"+def.gvr.Resource] = true
	}
	return out
}

func loadedKinds(defs []resourceDef) map[string]bool {
	out := map[string]bool{}
	for _, def := range defs {
		out[def.kind] = true
	}
	return out
}

type resourceLoadResult struct {
	objects []graph.Object
	err     string
}

// loadResources bounds concurrent API calls so a broad relationship profile
// remains quick on CRD-heavy clusters without flooding the apiserver.
func loadResources(ctx context.Context, dyn dynamic.Interface, namespace string, defs []resourceDef) ([]graph.Object, []string) {
	results := make([]resourceLoadResult, len(defs))
	semaphore := make(chan struct{}, 16)
	var wg sync.WaitGroup
	for i, def := range defs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				results[i].err = fmt.Sprintf("%s: %v", def.kind, ctx.Err())
				return
			}
			defer func() { <-semaphore }()

			var list *unstructured.UnstructuredList
			var err error
			if def.namespaced && !loadAcrossNamespaces(def.kind) {
				list, err = dyn.Resource(def.gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
			} else {
				list, err = dyn.Resource(def.gvr).List(ctx, metav1.ListOptions{})
			}
			if err != nil {
				if !isBenignMissingResource(def.kind, err) {
					results[i].err = fmt.Sprintf("%s: %v", def.kind, err)
				}
				return
			}
			results[i].objects = make([]graph.Object, 0, len(list.Items))
			for j := range list.Items {
				item := list.Items[j]
				results[i].objects = append(results[i].objects, objectFromUnstructured(def.kind, &item))
			}
		}()
	}
	wg.Wait()

	var objects []graph.Object
	var loadErrors []string
	for _, result := range results {
		objects = append(objects, result.objects...)
		if result.err != "" {
			loadErrors = append(loadErrors, result.err)
		}
	}
	return objects, loadErrors
}

func loadAcrossNamespaces(kind string) bool {
	return kind == "Application" || kind == "CatalogSource"
}

func discoverResourceTypes(config *rest.Config) []ResourceType {
	client, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil
	}
	lists, _ := client.ServerPreferredResources()
	return resourceTypesFromLists(lists)
}

func resourceTypesFromLists(lists []*metav1.APIResourceList) []ResourceType {
	seen := map[string]bool{}
	var out []ResourceType
	for _, list := range lists {
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			continue
		}
		for _, resource := range list.APIResources {
			if strings.Contains(resource.Name, "/") || !containsString([]string(resource.Verbs), "list") || resource.Kind == "" {
				continue
			}
			gvr := gv.WithResource(resource.Name)
			key := gvr.Group + "/" + gvr.Resource
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, ResourceType{GVR: gvr, Kind: resource.Kind, Namespaced: resource.Namespaced, ShortNames: resource.ShortNames})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind == out[j].Kind {
			return out[i].GVR.Group < out[j].GVR.Group
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func snapshotResourceDefs(resources []ResourceType, requestedKind string, allNamespaces bool) []resourceDef {
	preferred := map[string]ResourceType{}
	for _, resource := range resources {
		preferred[resource.GVR.Group+"/"+resource.GVR.Resource] = resource
	}
	defs := map[string]resourceDef{}
	add := func(def resourceDef) {
		key := def.gvr.Group + "/" + def.gvr.Resource
		if resource, ok := preferred[key]; ok {
			def.gvr = resource.GVR
			def.kind = resource.Kind
			def.namespaced = resource.Namespaced
		} else if len(resources) > 0 {
			return
		}
		defs[key] = def
	}
	for _, def := range phaseOneResources {
		add(def)
	}
	for key := range relationshipProfile {
		if resource, ok := preferred[key]; ok {
			add(resourceDef{gvr: resource.GVR, kind: resource.Kind, namespaced: resource.Namespaced})
		}
	}
	if requestedKind != "" {
		for _, resource := range resources {
			if resourceMatches(resource, requestedKind) {
				add(resourceDef{gvr: resource.GVR, kind: resource.Kind, namespaced: resource.Namespaced})
				break
			}
		}
	}
	if allNamespaces {
		requested := requestedKind
		if requested == "" {
			requested = "Pod"
		}
		for _, resource := range resources {
			if resourceMatches(resource, requested) {
				requested = resource.Kind
				break
			}
		}
		wanted := allNamespaceRelationKinds(requested)
		for key, def := range defs {
			if !wanted[def.kind] {
				delete(defs, key)
			}
		}
		for _, resource := range resources {
			if wanted[resource.Kind] {
				add(resourceDef{gvr: resource.GVR, kind: resource.Kind, namespaced: resource.Namespaced})
			}
		}
	}
	out := make([]resourceDef, 0, len(defs))
	for _, def := range defs {
		out = append(out, def)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].kind < out[j].kind })
	return out
}

func allNamespaceRelationKinds(requested string) map[string]bool {
	wanted := map[string]bool{requested: true}
	add := func(kinds ...string) {
		for _, kind := range kinds {
			wanted[kind] = true
		}
	}
	switch requested {
	case "Pod", "Deployment", "ReplicaSet", "StatefulSet", "DaemonSet", "Job", "CronJob", "Rollout", "DeploymentConfig":
		add("Pod", "Deployment", "ReplicaSet", "StatefulSet", "DaemonSet", "Job", "CronJob", "Service", "NetworkPolicy", "PodDisruptionBudget", "HorizontalPodAutoscaler", "ConfigMap", "Secret", "PersistentVolumeClaim", "ServiceAccount")
	case "Secret", "ConfigMap", "PersistentVolumeClaim", "ServiceAccount":
		add("Pod", "Deployment", "ReplicaSet", "StatefulSet", "DaemonSet", "Job", "CronJob")
	case "Service":
		add("Pod", "EndpointSlice", "Endpoints", "Ingress", "Route", "HTTPRoute", "GRPCRoute", "ServiceMonitor")
	case "Gateway", "HTTPRoute", "GRPCRoute":
		add("Gateway", "HTTPRoute", "GRPCRoute", "Service", "Secret", "ReferenceGrant")
	case "ExternalSecret", "SecretStore", "ClusterSecretStore":
		add("ExternalSecret", "SecretStore", "ClusterSecretStore", "Secret")
	case "Role", "ClusterRole", "RoleBinding", "ClusterRoleBinding":
		add("Role", "ClusterRole", "RoleBinding", "ClusterRoleBinding", "ServiceAccount")
	case "Kafka", "KafkaTopic", "KafkaUser", "KafkaConnect", "KafkaConnector":
		add("Kafka", "KafkaTopic", "KafkaUser", "KafkaConnect", "KafkaConnector")
	case "Application", "ApplicationSet":
		add("Application", "ApplicationSet", "Namespace")
	case "ClusterOperator", "ClusterVersion":
		add("ClusterOperator", "ClusterVersion")
	case "Certificate", "Issuer", "ClusterIssuer":
		add("Certificate", "Issuer", "ClusterIssuer", "Secret")
	case "ServiceMonitor", "PodMonitor", "PrometheusRule":
		add("ServiceMonitor", "PodMonitor", "PrometheusRule", "Service", "Pod")
	case "Subscription", "InstallPlan", "ClusterServiceVersion", "CatalogSource", "OperatorGroup":
		add("Subscription", "InstallPlan", "ClusterServiceVersion", "CatalogSource", "OperatorGroup")
	case "PersistentVolume", "StorageClass", "VolumeSnapshot":
		add("PersistentVolume", "PersistentVolumeClaim", "StorageClass", "VolumeSnapshot", "VolumeSnapshotClass")
	default:
		// Generic CRDs still get their conventional workload ownership
		// neighborhood without loading the entire cluster API surface.
		add("Pod", "Deployment", "ReplicaSet")
	}
	return wanted
}

func resourceMatches(resource ResourceType, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == strings.ToLower(resource.Kind) || query == strings.ToLower(resource.GVR.Resource) || query == strings.ToLower(resource.CommandName()) || query == strings.ToLower(resource.Kind+"."+resource.GVR.Group) {
		return true
	}
	for _, shortName := range resource.ShortNames {
		if query == strings.ToLower(shortName) {
			return true
		}
	}
	return false
}

func (r ResourceType) Key() string {
	return r.GVR.Group + "/" + r.GVR.Resource
}

func (r ResourceType) CommandName() string {
	if r.GVR.Group == "" {
		return r.GVR.Resource
	}
	return r.GVR.Resource + "." + r.GVR.Group
}

// ResolveResource returns a discovered resource by kind, plural resource
// name, or server-provided short name.
func (s Snapshot) ResolveResource(query string) (ResourceType, bool) {
	query = strings.ToLower(strings.TrimSpace(query))
	for _, resource := range s.Resources {
		if query == strings.ToLower(resource.CommandName()) || query == strings.ToLower(resource.Kind+"."+resource.GVR.Group) {
			return resource, true
		}
	}
	var match ResourceType
	found := false
	for _, resource := range s.Resources {
		if resourceMatches(resource, query) {
			if found && resource.Key() != match.Key() {
				return ResourceType{}, false
			}
			match = resource
			found = true
		}
	}
	return match, found
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
		metrics[PodMetricKey(item.GetNamespace(), name)] = PodMetric{CPUMilli: cpu, MemBytes: mem, Found: true}
	}
	return metrics
}

func PodMetricKey(namespace, name string) string {
	return namespace + "/" + name
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
		Group:     item.GroupVersionKind().Group,
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
	type eventRecord struct {
		at   string
		text string
	}
	records := map[string][]eventRecord{}
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "events"}
	list, err := dyn.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return map[string][]string{}
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
		at, _, _ := unstructured.NestedString(event.Object, "eventTime")
		if at == "" {
			at, _, _ = unstructured.NestedString(event.Object, "lastTimestamp")
		}
		if at == "" {
			at = event.GetCreationTimestamp().UTC().Format("2006-01-02T15:04:05Z")
		}
		key := kind + "/" + event.GetNamespace() + "/" + name
		records[key] = append(records[key], eventRecord{at: at, text: eventType + " " + reason + ": " + message})
	}
	events := map[string][]string{}
	for key, values := range records {
		sort.SliceStable(values, func(i, j int) bool { return values[i].at < values[j].at })
		if len(values) > 20 {
			values = values[len(values)-20:]
		}
		for _, value := range values {
			events[key] = append(events[key], value.text)
		}
	}
	return events
}

func attachEvents(objects []graph.Object, events map[string][]string) {
	for i := range objects {
		objects[i].Events = events[objects[i].Ref.IdentityKey()]
	}
}
