package graph

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

func Build(objects []Object) *Graph {
	sort.Slice(objects, func(i, j int) bool {
		if objects[i].Ref.Kind == objects[j].Ref.Kind {
			return objects[i].Ref.Name < objects[j].Ref.Name
		}
		return objects[i].Ref.Kind < objects[j].Ref.Kind
	})

	index := map[string]Object{}
	byUID := map[string]Object{}
	byKind := map[string][]Object{}
	for _, obj := range objects {
		index[obj.Ref.Key()] = obj
		byKind[obj.Ref.Kind] = append(byKind[obj.Ref.Kind], obj)
		if obj.Ref.UID != "" {
			byUID[string(obj.Ref.UID)] = obj
		}
	}

	var edges []Edge
	var problems []Problem

	for _, obj := range objects {
		for _, owner := range obj.Raw.GetOwnerReferences() {
			parent, ok := byUID[string(owner.UID)]
			if !ok {
				continue
			}
			edges = append(edges, Edge{
				From:   parent.Ref,
				To:     obj.Ref,
				Type:   "Owns",
				Health: "Healthy",
				Source: "metadata.ownerReferences",
				Reason: fmt.Sprintf("%s owns this object through ownerReferences uid=%s.", parent.Ref.Label(), owner.UID),
			})
		}

		switch obj.Ref.Kind {
		case "Service":
			selector, _, _ := unstructured.NestedStringMap(obj.Raw.Object, "spec", "selector")
			if len(selector) == 0 {
				continue
			}
			matches := matchingPods(byKind["Pod"], obj.Ref.Namespace, selector)
			for _, pod := range matches {
				edges = append(edges, Edge{
					From:   obj.Ref,
					To:     pod.Ref,
					Type:   "Selects",
					Health: "Healthy",
					Source: "spec.selector",
					Reason: "Service selector " + selectorString(selector) + " matches Pod labels.",
				})
			}
			if len(matches) == 0 {
				problems = append(problems, Problem{Object: obj.Ref, Level: "Broken", Message: "Service selector matches zero Pods."})
			}
			edges, problems = appendServiceEndpointEdges(edges, problems, obj, byKind["EndpointSlice"], byKind["Endpoints"])
		case "Ingress":
			edges, problems = appendIngressEdges(edges, problems, obj, index)
		case "Route":
			toName, _, _ := unstructured.NestedString(obj.Raw.Object, "spec", "to", "name")
			toKind, _, _ := unstructured.NestedString(obj.Raw.Object, "spec", "to", "kind")
			if toKind == "" || toKind == "Service" {
				edges, problems = appendNamedRef(edges, problems, obj, index, "Service", toName, "RoutesTo", "spec.to.name", "Route spec.to.name points to Service.")
			}
		case "Pod":
			edges, problems = appendPodRefs(edges, problems, obj, index)
			problems = append(problems, podProblems(obj)...)
		case "PersistentVolumeClaim":
			phase, _, _ := unstructured.NestedString(obj.Raw.Object, "status", "phase")
			if phase != "" && phase != "Bound" {
				problems = append(problems, Problem{Object: obj.Ref, Level: "Warning", Message: "PVC phase is " + phase + ", not Bound."})
			}
		case "NetworkPolicy":
			edges = appendNetworkPolicyEdges(edges, obj, byKind["Pod"])
		case "HorizontalPodAutoscaler":
			edges, problems = appendHPAEdges(edges, problems, obj, index)
		case "Certificate":
			edges, problems = appendCertificateEdges(edges, problems, obj, index)
			problems = append(problems, conditionProblems(obj)...)
		case "Deployment", "StatefulSet", "DaemonSet":
			problems = append(problems, workloadProblems(obj)...)
		case "Job":
			problems = append(problems, jobProblems(obj)...)
		case "Node":
			problems = append(problems, nodeProblems(obj)...)
		case "Subscription":
			edges = appendSubscriptionEdges(edges, obj, index)
		case "InstallPlan":
			edges = appendInstallPlanEdges(edges, obj, index)
		case "Application":
			edges = appendArgoApplicationEdges(edges, obj, objects)
		}
	}

	return New(objects, dedupeEdges(edges), problems)
}

func workloadProblems(obj Object) []Problem {
	ready, _, _ := unstructured.NestedInt64(obj.Raw.Object, "status", "readyReplicas")
	replicas, _, _ := unstructured.NestedInt64(obj.Raw.Object, "status", "replicas")
	if obj.Ref.Kind == "DaemonSet" {
		ready, _, _ = unstructured.NestedInt64(obj.Raw.Object, "status", "numberReady")
		replicas, _, _ = unstructured.NestedInt64(obj.Raw.Object, "status", "desiredNumberScheduled")
	}
	if replicas > 0 && ready < replicas {
		return []Problem{{Object: obj.Ref, Level: "Broken", Message: fmt.Sprintf("%d/%d replicas ready.", ready, replicas)}}
	}
	return nil
}

func jobProblems(obj Object) []Problem {
	failed, _, _ := unstructured.NestedInt64(obj.Raw.Object, "status", "failed")
	if failed > 0 {
		return []Problem{{Object: obj.Ref, Level: "Broken", Message: fmt.Sprintf("%d pod failures.", failed)}}
	}
	return nil
}

func nodeProblems(obj Object) []Problem {
	for _, problem := range conditionProblems(obj) {
		if strings.Contains(problem.Message, "Ready False") || strings.Contains(problem.Message, "Ready Unknown") {
			problem.Level = "Broken"
			return []Problem{problem}
		}
	}
	return nil
}

func conditionProblems(obj Object) []Problem {
	conditions, _, _ := unstructured.NestedSlice(obj.Raw.Object, "status", "conditions")
	var problems []Problem
	for _, condition := range conditions {
		c, ok := condition.(map[string]any)
		if !ok {
			continue
		}
		typ, _, _ := unstructured.NestedString(c, "type")
		status, _, _ := unstructured.NestedString(c, "status")
		reason, _, _ := unstructured.NestedString(c, "reason")
		message, _, _ := unstructured.NestedString(c, "message")
		if status == "True" && typ != "Ready" {
			if typ == "MemoryPressure" || typ == "DiskPressure" || typ == "PIDPressure" || typ == "NetworkUnavailable" {
				problems = append(problems, Problem{Object: obj.Ref, Level: "Warning", Message: conditionMessage(typ, status, reason, message)})
			}
			continue
		}
		if typ == "Ready" && status != "True" {
			problems = append(problems, Problem{Object: obj.Ref, Level: "Warning", Message: conditionMessage(typ, status, reason, message)})
		}
	}
	return problems
}

func conditionMessage(typ, status, reason, message string) string {
	out := typ + " " + status
	if reason != "" {
		out += " " + reason
	}
	if message != "" {
		out += ": " + message
	}
	return out + "."
}

func appendServiceEndpointEdges(edges []Edge, problems []Problem, service Object, endpointSlices, endpoints []Object) ([]Edge, []Problem) {
	hasEndpoint := false
	for _, slice := range endpointSlices {
		if slice.Ref.Namespace != service.Ref.Namespace {
			continue
		}
		if slice.Labels["kubernetes.io/service-name"] != service.Ref.Name {
			continue
		}
		hasEndpoint = true
		edges = append(edges, Edge{From: service.Ref, To: slice.Ref, Type: "HasEndpoints", Health: "Healthy", Source: "metadata.labels[kubernetes.io/service-name]", Reason: "EndpointSlice belongs to Service."})
	}
	for _, endpoint := range endpoints {
		if endpoint.Ref.Namespace != service.Ref.Namespace || endpoint.Ref.Name != service.Ref.Name {
			continue
		}
		hasEndpoint = true
		edges = append(edges, Edge{From: service.Ref, To: endpoint.Ref, Type: "HasEndpoints", Health: "Healthy", Source: "metadata.name", Reason: "Endpoints object belongs to Service."})
	}
	if !hasEndpoint {
		problems = append(problems, Problem{Object: service.Ref, Level: "Warning", Message: "Service has no EndpointSlice or Endpoints object loaded."})
	}
	return edges, problems
}

func appendNetworkPolicyEdges(edges []Edge, policy Object, pods []Object) []Edge {
	selector, _, _ := unstructured.NestedStringMap(policy.Raw.Object, "spec", "podSelector", "matchLabels")
	if len(selector) == 0 {
		return edges
	}
	for _, pod := range matchingPods(pods, policy.Ref.Namespace, selector) {
		edges = append(edges, Edge{From: policy.Ref, To: pod.Ref, Type: "Selects", Health: "Healthy", Source: "spec.podSelector", Reason: "NetworkPolicy podSelector matches Pod labels."})
	}
	return edges
}

func appendHPAEdges(edges []Edge, problems []Problem, hpa Object, index map[string]Object) ([]Edge, []Problem) {
	kind, _, _ := unstructured.NestedString(hpa.Raw.Object, "spec", "scaleTargetRef", "kind")
	name, _, _ := unstructured.NestedString(hpa.Raw.Object, "spec", "scaleTargetRef", "name")
	if kind == "" || name == "" {
		return edges, problems
	}
	return appendNamedRef(edges, problems, hpa, index, kind, name, "Scales", "spec.scaleTargetRef", "HPA scales target workload.")
}

func appendCertificateEdges(edges []Edge, problems []Problem, cert Object, index map[string]Object) ([]Edge, []Problem) {
	secretName, _, _ := unstructured.NestedString(cert.Raw.Object, "spec", "secretName")
	edges, problems = appendNamedRef(edges, problems, cert, index, "Secret", secretName, "WritesSecret", "spec.secretName", "Certificate writes TLS Secret.")
	issuerName, _, _ := unstructured.NestedString(cert.Raw.Object, "spec", "issuerRef", "name")
	issuerKind, _, _ := unstructured.NestedString(cert.Raw.Object, "spec", "issuerRef", "kind")
	if issuerKind == "" {
		issuerKind = "Issuer"
	}
	if issuerKind == "ClusterIssuer" {
		toRef := ObjectRef{Kind: "ClusterIssuer", Name: issuerName}
		to, ok := index[toRef.Key()]
		if ok {
			edges = append(edges, Edge{From: cert.Ref, To: to.Ref, Type: "UsesIssuer", Health: "Healthy", Source: "spec.issuerRef", Reason: "Certificate uses ClusterIssuer."})
		}
		return edges, problems
	}
	return appendNamedRef(edges, problems, cert, index, issuerKind, issuerName, "UsesIssuer", "spec.issuerRef", "Certificate uses issuer.")
}

func podProblems(pod Object) []Problem {
	var problems []Problem
	phase, _, _ := unstructured.NestedString(pod.Raw.Object, "status", "phase")
	if phase == "Failed" || phase == "Unknown" {
		problems = append(problems, Problem{Object: pod.Ref, Level: "Broken", Message: "Pod phase is " + phase + "."})
	}
	if phase == "Pending" {
		problems = append(problems, Problem{Object: pod.Ref, Level: "Warning", Message: "Pod is Pending; check scheduling and mount events."})
	}
	for _, path := range [][]string{{"status", "initContainerStatuses"}, {"status", "containerStatuses"}} {
		statuses, _, _ := unstructured.NestedSlice(pod.Raw.Object, path...)
		for _, status := range statuses {
			container, ok := status.(map[string]any)
			if !ok {
				continue
			}
			name, _, _ := unstructured.NestedString(container, "name")
			waitingReason, _, _ := unstructured.NestedString(container, "state", "waiting", "reason")
			if waitingReason != "" {
				problems = append(problems, Problem{Object: pod.Ref, Level: problemLevel(waitingReason), Message: fmt.Sprintf("%s waiting: %s.", name, waitingReason)})
			}
			termReason, _, _ := unstructured.NestedString(container, "lastState", "terminated", "reason")
			exitCode, _, _ := unstructured.NestedInt64(container, "lastState", "terminated", "exitCode")
			if termReason != "" && exitCode != 0 {
				problems = append(problems, Problem{Object: pod.Ref, Level: "Broken", Message: fmt.Sprintf("%s last exit: %s code %d.", name, termReason, exitCode)})
			}
		}
	}
	return problems
}

func problemLevel(reason string) string {
	switch strings.ToLower(reason) {
	case "crashloopbackoff", "imagepullbackoff", "errimagepull", "createcontainerconfigerror", "createcontainererror":
		return "Broken"
	default:
		return "Warning"
	}
}

func appendIngressEdges(edges []Edge, problems []Problem, obj Object, index map[string]Object) ([]Edge, []Problem) {
	defaultBackend, ok, _ := unstructured.NestedMap(obj.Raw.Object, "spec", "defaultBackend", "service")
	if ok {
		name, _ := defaultBackend["name"].(string)
		edges, problems = appendNamedRef(edges, problems, obj, index, "Service", name, "RoutesTo", "spec.defaultBackend.service.name", "Ingress default backend points to Service.")
	}
	rules, _, _ := unstructured.NestedSlice(obj.Raw.Object, "spec", "rules")
	for _, rule := range rules {
		ruleMap, ok := rule.(map[string]any)
		if !ok {
			continue
		}
		paths, _, _ := unstructured.NestedSlice(ruleMap, "http", "paths")
		for _, path := range paths {
			pathMap, ok := path.(map[string]any)
			if !ok {
				continue
			}
			name, _, _ := unstructured.NestedString(pathMap, "backend", "service", "name")
			edges, problems = appendNamedRef(edges, problems, obj, index, "Service", name, "RoutesTo", "spec.rules.http.paths.backend.service.name", "Ingress backend points to Service.")
		}
	}
	return edges, problems
}

func appendPodRefs(edges []Edge, problems []Problem, pod Object, index map[string]Object) ([]Edge, []Problem) {
	sa, _, _ := unstructured.NestedString(pod.Raw.Object, "spec", "serviceAccountName")
	if sa == "" {
		sa = "default"
	}
	edges, problems = appendNamedRef(edges, problems, pod, index, "ServiceAccount", sa, "UsesServiceAccount", "spec.serviceAccountName", "Pod uses ServiceAccount.")

	volumes, _, _ := unstructured.NestedSlice(pod.Raw.Object, "spec", "volumes")
	for _, volume := range volumes {
		v, ok := volume.(map[string]any)
		if !ok {
			continue
		}
		if name, _, _ := unstructured.NestedString(v, "configMap", "name"); name != "" {
			edges, problems = appendNamedRef(edges, problems, pod, index, "ConfigMap", name, "Mounts", "spec.volumes.configMap.name", "Pod volume mounts ConfigMap.")
		}
		if name, _, _ := unstructured.NestedString(v, "secret", "secretName"); name != "" {
			edges, problems = appendNamedRef(edges, problems, pod, index, "Secret", name, "Mounts", "spec.volumes.secret.secretName", "Pod volume mounts Secret.")
		}
		if name, _, _ := unstructured.NestedString(v, "persistentVolumeClaim", "claimName"); name != "" {
			edges, problems = appendNamedRef(edges, problems, pod, index, "PersistentVolumeClaim", name, "Mounts", "spec.volumes.persistentVolumeClaim.claimName", "Pod volume mounts PVC.")
		}
	}

	for _, path := range [][]string{{"spec", "containers"}, {"spec", "initContainers"}} {
		containers, _, _ := unstructured.NestedSlice(pod.Raw.Object, path...)
		for _, container := range containers {
			c, ok := container.(map[string]any)
			if !ok {
				continue
			}
			edges, problems = appendContainerRefs(edges, problems, pod, index, c)
		}
	}
	return edges, problems
}

func appendContainerRefs(edges []Edge, problems []Problem, pod Object, index map[string]Object, container map[string]any) ([]Edge, []Problem) {
	envFrom, _, _ := unstructured.NestedSlice(container, "envFrom")
	for _, item := range envFrom {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if name, _, _ := unstructured.NestedString(m, "configMapRef", "name"); name != "" {
			edges, problems = appendNamedRef(edges, problems, pod, index, "ConfigMap", name, "UsesEnv", "spec.containers.envFrom.configMapRef.name", "Pod envFrom uses ConfigMap.")
		}
		if name, _, _ := unstructured.NestedString(m, "secretRef", "name"); name != "" {
			edges, problems = appendNamedRef(edges, problems, pod, index, "Secret", name, "UsesEnv", "spec.containers.envFrom.secretRef.name", "Pod envFrom uses Secret.")
		}
	}
	env, _, _ := unstructured.NestedSlice(container, "env")
	for _, item := range env {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if name, _, _ := unstructured.NestedString(m, "valueFrom", "configMapKeyRef", "name"); name != "" {
			edges, problems = appendNamedRef(edges, problems, pod, index, "ConfigMap", name, "UsesEnv", "spec.containers.env.valueFrom.configMapKeyRef.name", "Pod env var uses ConfigMap key.")
		}
		if name, _, _ := unstructured.NestedString(m, "valueFrom", "secretKeyRef", "name"); name != "" {
			edges, problems = appendNamedRef(edges, problems, pod, index, "Secret", name, "UsesEnv", "spec.containers.env.valueFrom.secretKeyRef.name", "Pod env var uses Secret key.")
		}
	}
	return edges, problems
}

func appendNamedRef(edges []Edge, problems []Problem, from Object, index map[string]Object, kind, name, edgeType, source, reason string) ([]Edge, []Problem) {
	if name == "" {
		return edges, problems
	}
	toRef := ObjectRef{Kind: kind, Namespace: from.Ref.Namespace, Name: name}
	to, ok := index[toRef.Key()]
	if !ok {
		problems = append(problems, Problem{Object: from.Ref, Level: "Broken", Message: fmt.Sprintf("%s references missing %s/%s.", from.Ref.Label(), kind, name)})
		return edges, problems
	}
	edges = append(edges, Edge{From: from.Ref, To: to.Ref, Type: edgeType, Health: "Healthy", Source: source, Reason: reason})
	return edges, problems
}

// appendSubscriptionEdges links an OLM Subscription to the InstallPlan and
// ClusterServiceVersion it produced. Both status fields are set by OLM only
// once resolution/install has actually happened, so an empty field (fresh
// or still-resolving Subscription) is normal, not a problem — and a
// populated field the graph can't resolve (RBAC hid the target, or it just
// hasn't loaded into this snapshot) isn't flagged as Broken either, unlike
// appendNamedRef's normal workload-reference handling: OLM's own resources
// are often intentionally excluded from a user's RBAC even when the
// Subscription itself is visible.
func appendSubscriptionEdges(edges []Edge, sub Object, index map[string]Object) []Edge {
	if name, _, _ := unstructured.NestedString(sub.Raw.Object, "status", "installPlanRef", "name"); name != "" {
		namespace, _, _ := unstructured.NestedString(sub.Raw.Object, "status", "installPlanRef", "namespace")
		if namespace == "" {
			namespace = sub.Ref.Namespace
		}
		if to, ok := index[(ObjectRef{Kind: "InstallPlan", Namespace: namespace, Name: name}).Key()]; ok {
			edges = append(edges, Edge{From: sub.Ref, To: to.Ref, Type: "Resolves", Health: "Healthy", Source: "status.installPlanRef", Reason: "Subscription resolved this InstallPlan."})
		}
	}
	if name, _, _ := unstructured.NestedString(sub.Raw.Object, "status", "installedCSV"); name != "" {
		if to, ok := index[(ObjectRef{Kind: "ClusterServiceVersion", Namespace: sub.Ref.Namespace, Name: name}).Key()]; ok {
			edges = append(edges, Edge{From: sub.Ref, To: to.Ref, Type: "Installs", Health: "Healthy", Source: "status.installedCSV", Reason: "Subscription installed this ClusterServiceVersion."})
		}
	}
	return edges
}

// appendInstallPlanEdges links an OLM InstallPlan to the CSV(s) it installs
// (spec.clusterServiceVersionNames). Same reasoning as
// appendSubscriptionEdges applies to unresolved names — no problems raised.
func appendInstallPlanEdges(edges []Edge, plan Object, index map[string]Object) []Edge {
	names, _, _ := unstructured.NestedStringSlice(plan.Raw.Object, "spec", "clusterServiceVersionNames")
	for _, name := range names {
		if to, ok := index[(ObjectRef{Kind: "ClusterServiceVersion", Namespace: plan.Ref.Namespace, Name: name}).Key()]; ok {
			edges = append(edges, Edge{From: plan.Ref, To: to.Ref, Type: "Installs", Health: "Healthy", Source: "spec.clusterServiceVersionNames", Reason: "InstallPlan installs this ClusterServiceVersion."})
		}
	}
	return edges
}

// argoInstanceLabel is the label ArgoCD stamps on every object it manages,
// naming the Application that owns it — the same label
// gitopsManagedByLine (internal/tui/model.go) already reads for the
// "managed-by: argocd" line. Reused here so the label key lives in exactly
// one place... except it doesn't; kept as a literal in both packages since
// graph doesn't import tui and there's no shared constants file yet.
const argoInstanceLabel = "argocd.argoproj.io/instance"

// appendArgoApplicationEdges links an ArgoCD Application to every
// same-namespace object carrying its argocd.argoproj.io/instance label —
// that label is how ArgoCD itself tracks ownership, there's no ownerRef or
// spec field to read. No problem is raised when nothing matches (a fresh
// Application that hasn't synced yet, or whose managed objects live in a
// namespace this snapshot didn't load).
func appendArgoApplicationEdges(edges []Edge, app Object, objects []Object) []Edge {
	for _, obj := range objects {
		if obj.Ref.Kind == "Application" || obj.Ref.Namespace != app.Ref.Namespace {
			continue
		}
		if obj.Raw.GetLabels()[argoInstanceLabel] != app.Ref.Name {
			continue
		}
		edges = append(edges, Edge{From: app.Ref, To: obj.Ref, Type: "Manages", Health: "Healthy", Source: "labels." + argoInstanceLabel, Reason: "ArgoCD Application manages this object."})
	}
	return edges
}

func matchingPods(pods []Object, namespace string, selector map[string]string) []Object {
	labelSelector := labels.SelectorFromSet(selector)
	var out []Object
	for _, pod := range pods {
		if pod.Ref.Namespace == namespace && labelSelector.Matches(labels.Set(pod.Labels)) {
			out = append(out, pod)
		}
	}
	return out
}

func selectorString(selector map[string]string) string {
	pairs := make([]string, 0, len(selector))
	for k, v := range selector {
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}

func dedupeEdges(edges []Edge) []Edge {
	seen := map[string]bool{}
	out := make([]Edge, 0, len(edges))
	for _, edge := range edges {
		key := edge.From.Key() + "|" + edge.To.Key() + "|" + edge.Type + "|" + edge.Source
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, edge)
	}
	return out
}
