package graph

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	byArgoInstance := map[string][]Object{}
	for _, obj := range objects {
		index[obj.Ref.Key()] = obj
		byKind[obj.Ref.Kind] = append(byKind[obj.Ref.Kind], obj)
		if obj.Ref.UID != "" {
			byUID[string(obj.Ref.UID)] = obj
		}
		if app := argoApplicationName(obj); app != "" {
			byArgoInstance[app] = append(byArgoInstance[app], obj)
		}
	}
	addUnambiguousObjectAliases(index, objects)

	var edges []Edge
	var problems []Problem

	for _, obj := range objects {
		edgeStart := len(edges)
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
		case "ResourceQuota":
			problems = append(problems, quotaProblems(obj)...)
		case "PersistentVolumeClaim":
			phase, _, _ := unstructured.NestedString(obj.Raw.Object, "status", "phase")
			if phase != "" && phase != "Bound" {
				problems = append(problems, Problem{Object: obj.Ref, Level: "Warning", Message: "PVC phase is " + phase + ", not Bound."})
			}
			edges = appendPVCRefs(edges, obj, index)
		case "PersistentVolume":
			edges = appendPVRefs(edges, obj, index)
		case "VolumeSnapshot":
			edges = appendVolumeSnapshotRefs(edges, obj, index)
		case "ServiceAccount":
			edges = appendServiceAccountRefs(edges, obj, index)
		case "RoleBinding", "ClusterRoleBinding":
			edges = appendRoleBindingRefs(edges, obj, index)
		case "NetworkPolicy":
			edges = appendNetworkPolicyEdges(edges, obj, byKind["Pod"])
		case "PodDisruptionBudget":
			edges = appendSelectorEdges(edges, obj, byKind["Pod"], "Protects", "spec.selector")
		case "HorizontalPodAutoscaler":
			edges, problems = appendHPAEdges(edges, problems, obj, index)
		case "Certificate":
			edges, problems = appendCertificateEdges(edges, problems, obj, index)
			problems = append(problems, conditionProblems(obj)...)
			problems = append(problems, certificateExpiryProblems(obj)...)
		case "Deployment", "StatefulSet", "DaemonSet", "DeploymentConfig", "Rollout":
			edges, problems = appendPodTemplateRefs(edges, problems, obj, index, "spec", "template", "spec")
			problems = append(problems, workloadProblems(obj)...)
		case "Job":
			edges, problems = appendPodTemplateRefs(edges, problems, obj, index, "spec", "template", "spec")
			problems = append(problems, jobProblems(obj)...)
		case "CronJob":
			edges, problems = appendPodTemplateRefs(edges, problems, obj, index, "spec", "jobTemplate", "spec", "template", "spec")
		case "Node":
			problems = append(problems, nodeProblems(obj)...)
		case "Subscription":
			edges = appendSubscriptionEdges(edges, obj, index)
		case "InstallPlan":
			edges = appendInstallPlanEdges(edges, obj, index)
		case "Application":
			managed := append([]Object{}, byArgoInstance[obj.Ref.Name]...)
			// Argo's resource-tracking label is normally just the
			// Application name, but namespaced tracking can use
			// <application-namespace>/<application-name>.
			managed = append(managed, byArgoInstance[obj.Ref.Namespace+"/"+obj.Ref.Name]...)
			edges = appendArgoApplicationEdges(edges, obj, managed)
			problems = append(problems, applicationProblems(obj)...)
		case "Gateway":
			edges = appendGatewayRefs(edges, obj, index)
		case "HTTPRoute", "GRPCRoute":
			edges = appendGatewayRouteRefs(edges, obj, index)
		case "ExternalSecret":
			edges = appendExternalSecretRefs(edges, obj, index)
		case "ServiceMonitor":
			edges = appendMonitorSelectorEdges(edges, obj, byKind["Service"])
		case "PodMonitor":
			edges = appendMonitorSelectorEdges(edges, obj, byKind["Pod"])
		case "ScaledObject":
			edges = appendScaledObjectRefs(edges, obj, index)
		case "KafkaTopic", "KafkaUser":
			edges = appendLabelRef(edges, obj, index, "strimzi.io/cluster", "Kafka", "BelongsTo")
		case "KafkaConnector":
			edges = appendLabelRef(edges, obj, index, "strimzi.io/cluster", "KafkaConnect", "BelongsTo")
		}
		problems = append(problems, genericConditionProblems(obj)...)
		problems = append(problems, genericPhaseProblems(obj)...)
		if shouldScanGenericRefs(obj.Ref.Kind) {
			edges = appendGenericRefs(edges, obj, index, edgeStart)
		}
	}

	return New(objects, dedupeEdges(edges), dedupeProblems(problems))
}

func shouldScanGenericRefs(kind string) bool {
	switch kind {
	case "ClusterRole", "Role", "ConfigMap", "Secret", "Event", "Node", "ResourceQuota", "LimitRange", "Namespace", "Project", "StorageClass":
		return false
	}
	return true
}

func addUnambiguousObjectAliases(index map[string]Object, objects []Object) {
	ambiguous := map[string]bool{}
	for _, obj := range objects {
		alias := obj.Ref.IdentityKey()
		if existing, ok := index[alias]; ok && existing.Ref.Key() != obj.Ref.Key() {
			ambiguous[alias] = true
			continue
		}
		index[alias] = obj
	}
	for alias := range ambiguous {
		delete(index, alias)
	}
}

func appendPodTemplateRefs(edges []Edge, problems []Problem, workload Object, index map[string]Object, path ...string) ([]Edge, []Problem) {
	spec, ok, _ := unstructured.NestedMap(workload.Raw.Object, path...)
	if !ok {
		return edges, problems
	}
	pseudo := workload
	pseudo.Raw = &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	return appendPodRefs(edges, problems, pseudo, index)
}

func appendServiceAccountRefs(edges []Edge, sa Object, index map[string]Object) []Edge {
	for _, path := range [][]string{{"secrets"}, {"imagePullSecrets"}} {
		refs, _, _ := unstructured.NestedSlice(sa.Raw.Object, path...)
		for _, ref := range refs {
			m, ok := ref.(map[string]any)
			if !ok {
				continue
			}
			name, _, _ := unstructured.NestedString(m, "name")
			edges = appendExistingRef(edges, sa, index, ObjectRef{Kind: "Secret", Namespace: sa.Ref.Namespace, Name: name}, "UsesSecret", "spec."+path[0], "ServiceAccount uses Secret.")
		}
	}
	return edges
}

func appendRoleBindingRefs(edges []Edge, binding Object, index map[string]Object) []Edge {
	roleKind, _, _ := unstructured.NestedString(binding.Raw.Object, "roleRef", "kind")
	roleName, _, _ := unstructured.NestedString(binding.Raw.Object, "roleRef", "name")
	roleNS := binding.Ref.Namespace
	if roleKind == "ClusterRole" {
		roleNS = ""
	}
	edges = appendExistingRef(edges, binding, index, ObjectRef{Kind: roleKind, Namespace: roleNS, Name: roleName}, "GrantsRole", "roleRef", "RoleBinding grants role.")
	subjects, _, _ := unstructured.NestedSlice(binding.Raw.Object, "subjects")
	for _, subject := range subjects {
		m, ok := subject.(map[string]any)
		if !ok {
			continue
		}
		kind, _, _ := unstructured.NestedString(m, "kind")
		name, _, _ := unstructured.NestedString(m, "name")
		namespace, _, _ := unstructured.NestedString(m, "namespace")
		if kind == "ServiceAccount" {
			if namespace == "" {
				namespace = binding.Ref.Namespace
			}
			edges = appendExistingRef(edges, binding, index, ObjectRef{Kind: kind, Namespace: namespace, Name: name}, "GrantsTo", "subjects", "RoleBinding grants permissions to ServiceAccount.")
		}
	}
	return edges
}

func appendPVCRefs(edges []Edge, pvc Object, index map[string]Object) []Edge {
	volume, _, _ := unstructured.NestedString(pvc.Raw.Object, "spec", "volumeName")
	edges = appendExistingRef(edges, pvc, index, ObjectRef{Kind: "PersistentVolume", Name: volume}, "BoundTo", "spec.volumeName", "PVC is bound to PersistentVolume.")
	class, _, _ := unstructured.NestedString(pvc.Raw.Object, "spec", "storageClassName")
	return appendExistingRef(edges, pvc, index, ObjectRef{Kind: "StorageClass", Name: class}, "UsesStorageClass", "spec.storageClassName", "PVC uses StorageClass.")
}

func appendPVRefs(edges []Edge, pv Object, index map[string]Object) []Edge {
	class, _, _ := unstructured.NestedString(pv.Raw.Object, "spec", "storageClassName")
	edges = appendExistingRef(edges, pv, index, ObjectRef{Kind: "StorageClass", Name: class}, "UsesStorageClass", "spec.storageClassName", "PersistentVolume uses StorageClass.")
	name, _, _ := unstructured.NestedString(pv.Raw.Object, "spec", "claimRef", "name")
	namespace, _, _ := unstructured.NestedString(pv.Raw.Object, "spec", "claimRef", "namespace")
	return appendExistingRef(edges, pv, index, ObjectRef{Kind: "PersistentVolumeClaim", Namespace: namespace, Name: name}, "BoundTo", "spec.claimRef", "PersistentVolume is bound to PVC.")
}

func appendVolumeSnapshotRefs(edges []Edge, snapshot Object, index map[string]Object) []Edge {
	name, _, _ := unstructured.NestedString(snapshot.Raw.Object, "spec", "source", "persistentVolumeClaimName")
	edges = appendExistingRef(edges, snapshot, index, ObjectRef{Kind: "PersistentVolumeClaim", Namespace: snapshot.Ref.Namespace, Name: name}, "Snapshots", "spec.source.persistentVolumeClaimName", "VolumeSnapshot snapshots PVC.")
	class, _, _ := unstructured.NestedString(snapshot.Raw.Object, "spec", "volumeSnapshotClassName")
	return appendExistingRef(edges, snapshot, index, ObjectRef{Kind: "VolumeSnapshotClass", Name: class}, "UsesSnapshotClass", "spec.volumeSnapshotClassName", "VolumeSnapshot uses VolumeSnapshotClass.")
}

func appendSelectorEdges(edges []Edge, from Object, candidates []Object, edgeType, source string) []Edge {
	selectorMap, ok, _ := unstructured.NestedMap(from.Raw.Object, strings.Split(source, ".")...)
	if !ok {
		return edges
	}
	selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
		MatchLabels:      stringMap(selectorMap["matchLabels"]),
		MatchExpressions: labelSelectorRequirements(selectorMap["matchExpressions"]),
	})
	if err != nil {
		return edges
	}
	for _, candidate := range candidates {
		if candidate.Ref.Namespace == from.Ref.Namespace && selector.Matches(labels.Set(candidate.Labels)) {
			edges = append(edges, Edge{From: from.Ref, To: candidate.Ref, Type: edgeType, Health: "Healthy", Source: source, Reason: from.Ref.Kind + " selector matches " + candidate.Ref.Kind + " labels."})
		}
	}
	return edges
}

func appendMonitorSelectorEdges(edges []Edge, monitor Object, candidates []Object) []Edge {
	selectorMap, ok, _ := unstructured.NestedMap(monitor.Raw.Object, "spec", "selector")
	if !ok {
		return edges
	}
	selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
		MatchLabels:      stringMap(selectorMap["matchLabels"]),
		MatchExpressions: labelSelectorRequirements(selectorMap["matchExpressions"]),
	})
	if err != nil {
		return edges
	}
	matchAny, _, _ := unstructured.NestedBool(monitor.Raw.Object, "spec", "namespaceSelector", "any")
	matchNames, _, _ := unstructured.NestedStringSlice(monitor.Raw.Object, "spec", "namespaceSelector", "matchNames")
	allowedNamespaces := map[string]bool{}
	for _, namespace := range matchNames {
		allowedNamespaces[namespace] = true
	}
	for _, candidate := range candidates {
		namespaceMatches := candidate.Ref.Namespace == monitor.Ref.Namespace
		if matchAny {
			namespaceMatches = true
		} else if len(allowedNamespaces) > 0 {
			namespaceMatches = allowedNamespaces[candidate.Ref.Namespace]
		}
		if namespaceMatches && selector.Matches(labels.Set(candidate.Labels)) {
			edges = append(edges, Edge{From: monitor.Ref, To: candidate.Ref, Type: "Monitors", Health: "Healthy", Source: "spec.selector", Reason: monitor.Ref.Kind + " selector matches " + candidate.Ref.Kind + " labels."})
		}
	}
	return edges
}

func stringMap(value any) map[string]string {
	in, _ := value.(map[string]any)
	out := map[string]string{}
	for key, value := range in {
		if text, ok := value.(string); ok {
			out[key] = text
		}
	}
	return out
}

func labelSelectorRequirements(value any) []metav1.LabelSelectorRequirement {
	items, _ := value.([]any)
	var out []metav1.LabelSelectorRequirement
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		key, _ := m["key"].(string)
		op, _ := m["operator"].(string)
		var values []string
		for _, value := range anySlice(m["values"]) {
			if text, ok := value.(string); ok {
				values = append(values, text)
			}
		}
		out = append(out, metav1.LabelSelectorRequirement{Key: key, Operator: metav1.LabelSelectorOperator(op), Values: values})
	}
	return out
}

func anySlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func appendGatewayRefs(edges []Edge, gateway Object, index map[string]Object) []Edge {
	listeners, _, _ := unstructured.NestedSlice(gateway.Raw.Object, "spec", "listeners")
	for _, listener := range listeners {
		m, ok := listener.(map[string]any)
		if !ok {
			continue
		}
		refs, _, _ := unstructured.NestedSlice(m, "tls", "certificateRefs")
		for _, ref := range refs {
			r, ok := ref.(map[string]any)
			if !ok {
				continue
			}
			kind, _, _ := unstructured.NestedString(r, "kind")
			if kind == "" {
				kind = "Secret"
			}
			name, _, _ := unstructured.NestedString(r, "name")
			namespace, _, _ := unstructured.NestedString(r, "namespace")
			if namespace == "" {
				namespace = gateway.Ref.Namespace
			}
			edges = appendExistingRef(edges, gateway, index, ObjectRef{Kind: kind, Namespace: namespace, Name: name}, "UsesCertificate", "spec.listeners.tls.certificateRefs", "Gateway listener uses certificate reference.")
		}
	}
	return edges
}

func appendGatewayRouteRefs(edges []Edge, route Object, index map[string]Object) []Edge {
	parents, _, _ := unstructured.NestedSlice(route.Raw.Object, "spec", "parentRefs")
	for _, parent := range parents {
		m, ok := parent.(map[string]any)
		if !ok {
			continue
		}
		kind, _, _ := unstructured.NestedString(m, "kind")
		if kind == "" {
			kind = "Gateway"
		}
		name, _, _ := unstructured.NestedString(m, "name")
		namespace, _, _ := unstructured.NestedString(m, "namespace")
		if namespace == "" {
			namespace = route.Ref.Namespace
		}
		edges = appendExistingRef(edges, route, index, ObjectRef{Kind: kind, Namespace: namespace, Name: name}, "AttachesTo", "spec.parentRefs", "Route attaches to Gateway.")
	}
	rules, _, _ := unstructured.NestedSlice(route.Raw.Object, "spec", "rules")
	for _, rule := range rules {
		ruleMap, ok := rule.(map[string]any)
		if !ok {
			continue
		}
		refs, _, _ := unstructured.NestedSlice(ruleMap, "backendRefs")
		for _, backend := range refs {
			m, ok := backend.(map[string]any)
			if !ok {
				continue
			}
			kind, _, _ := unstructured.NestedString(m, "kind")
			if kind == "" {
				kind = "Service"
			}
			name, _, _ := unstructured.NestedString(m, "name")
			namespace, _, _ := unstructured.NestedString(m, "namespace")
			if namespace == "" {
				namespace = route.Ref.Namespace
			}
			edges = appendExistingRef(edges, route, index, ObjectRef{Kind: kind, Namespace: namespace, Name: name}, "RoutesTo", "spec.rules.backendRefs", "Gateway API route sends traffic to backend.")
		}
	}
	return edges
}

func appendExternalSecretRefs(edges []Edge, external Object, index map[string]Object) []Edge {
	name, _, _ := unstructured.NestedString(external.Raw.Object, "spec", "target", "name")
	if name == "" {
		name = external.Ref.Name
	}
	edges = appendExistingRef(edges, external, index, ObjectRef{Kind: "Secret", Namespace: external.Ref.Namespace, Name: name}, "WritesSecret", "spec.target.name", "ExternalSecret materializes target Secret.")
	storeKind, _, _ := unstructured.NestedString(external.Raw.Object, "spec", "secretStoreRef", "kind")
	if storeKind == "" {
		storeKind = "SecretStore"
	}
	storeName, _, _ := unstructured.NestedString(external.Raw.Object, "spec", "secretStoreRef", "name")
	storeNS := external.Ref.Namespace
	if storeKind == "ClusterSecretStore" {
		storeNS = ""
	}
	return appendExistingRef(edges, external, index, ObjectRef{Kind: storeKind, Namespace: storeNS, Name: storeName}, "UsesSecretStore", "spec.secretStoreRef", "ExternalSecret reads from secret store.")
}

func appendScaledObjectRefs(edges []Edge, scaled Object, index map[string]Object) []Edge {
	kind, _, _ := unstructured.NestedString(scaled.Raw.Object, "spec", "scaleTargetRef", "kind")
	if kind == "" {
		kind = "Deployment"
	}
	name, _, _ := unstructured.NestedString(scaled.Raw.Object, "spec", "scaleTargetRef", "name")
	edges = appendExistingRef(edges, scaled, index, ObjectRef{Kind: kind, Namespace: scaled.Ref.Namespace, Name: name}, "Scales", "spec.scaleTargetRef", "ScaledObject scales workload.")
	triggers, _, _ := unstructured.NestedSlice(scaled.Raw.Object, "spec", "triggers")
	for _, trigger := range triggers {
		m, ok := trigger.(map[string]any)
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(m, "authenticationRef", "name")
		kind, _, _ := unstructured.NestedString(m, "authenticationRef", "kind")
		if kind == "" {
			kind = "TriggerAuthentication"
		}
		namespace := scaled.Ref.Namespace
		if kind == "ClusterTriggerAuthentication" {
			namespace = ""
		}
		edges = appendExistingRef(edges, scaled, index, ObjectRef{Kind: kind, Namespace: namespace, Name: name}, "UsesAuthentication", "spec.triggers.authenticationRef", "ScaledObject trigger uses authentication.")
	}
	return edges
}

func appendLabelRef(edges []Edge, from Object, index map[string]Object, label, kind, edgeType string) []Edge {
	name := from.Labels[label]
	return appendExistingRef(edges, from, index, ObjectRef{Kind: kind, Namespace: from.Ref.Namespace, Name: name}, edgeType, "metadata.labels["+label+"]", from.Ref.Kind+" belongs to "+kind+" named by label.")
}

func appendExistingRef(edges []Edge, from Object, index map[string]Object, ref ObjectRef, edgeType, source, reason string) []Edge {
	if ref.Kind == "" || ref.Name == "" || ref.Key() == from.Ref.Key() {
		return edges
	}
	to, ok := index[ref.Key()]
	if !ok && ref.Namespace != "" {
		to, ok = index[(ObjectRef{Group: ref.Group, Kind: ref.Kind, Name: ref.Name}).Key()]
	}
	if !ok {
		return edges
	}
	return append(edges, Edge{From: from.Ref, To: to.Ref, Type: edgeType, Health: "Healthy", Source: source, Reason: reason})
}

// appendGenericRefs recognizes Kubernetes' conventional reference shapes in
// any built-in or CRD. It only creates edges to objects actually loaded in
// the graph, avoiding false missing-reference alarms for arbitrary fields.
func appendGenericRefs(edges []Edge, from Object, index map[string]Object, edgeStart int) []Edge {
	existingTargets := map[string]bool{}
	for _, edge := range edges[edgeStart:] {
		if edge.From.Key() == from.Ref.Key() {
			existingTargets[edge.To.Key()] = true
		}
	}
	var walk func(value any, path []string, field string)
	walk = func(value any, path []string, field string) {
		if len(path) > 0 && path[0] == "metadata" {
			return
		}
		switch typed := value.(type) {
		case map[string]any:
			kind, _ := typed["kind"].(string)
			name, _ := typed["name"].(string)
			namespace, _ := typed["namespace"].(string)
			group, _ := typed["apiGroup"].(string)
			if group == "" {
				group, _ = typed["group"].(string)
			}
			if group == "" {
				if apiVersion, ok := typed["apiVersion"].(string); ok && strings.Contains(apiVersion, "/") {
					group = strings.SplitN(apiVersion, "/", 2)[0]
				}
			}
			if name != "" {
				if kind == "" {
					kind = inferredReferenceKind(field)
				}
				if namespace == "" && !clusterScopedReferenceKind(kind) {
					namespace = from.Ref.Namespace
				}
				edges = appendGenericExistingRef(edges, from, index, ObjectRef{Group: group, Kind: kind, Namespace: namespace, Name: name}, strings.Join(path, "."), existingTargets)
			}
			for key, child := range typed {
				if kind := inferredScalarReferenceKind(key); kind != "" {
					if name, ok := child.(string); ok {
						ns := from.Ref.Namespace
						if clusterScopedReferenceKind(kind) {
							ns = ""
						}
						edges = appendGenericExistingRef(edges, from, index, ObjectRef{Kind: kind, Namespace: ns, Name: name}, strings.Join(append(path, key), "."), existingTargets)
					}
				}
				walk(child, append(path, key), key)
			}
		case []any:
			for _, child := range typed {
				walk(child, path, field)
			}
		}
	}
	for _, root := range []string{"spec", "status"} {
		if value, ok := from.Raw.Object[root]; ok {
			walk(value, []string{root}, root)
		}
	}
	return edges
}

func appendGenericExistingRef(edges []Edge, from Object, index map[string]Object, ref ObjectRef, source string, existingTargets map[string]bool) []Edge {
	to, ok := index[ref.Key()]
	if !ok && ref.Namespace != "" {
		to, ok = index[(ObjectRef{Group: ref.Group, Kind: ref.Kind, Name: ref.Name}).Key()]
	}
	if !ok || to.Ref.Key() == from.Ref.Key() {
		return edges
	}
	if existingTargets[to.Ref.Key()] {
		return edges
	}
	existingTargets[to.Ref.Key()] = true
	reason := from.Ref.Kind + " references " + ref.Kind + " through " + source + "."
	return append(edges, Edge{From: from.Ref, To: to.Ref, Type: "References", Health: "Healthy", Source: source, Reason: reason})
}

func inferredReferenceKind(field string) string {
	field = strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(field, "Refs"), "Ref"))
	switch field {
	case "secret", "certificateref":
		return "Secret"
	case "configmap":
		return "ConfigMap"
	case "service", "backend":
		return "Service"
	case "serviceaccount":
		return "ServiceAccount"
	case "persistentvolumeclaim", "claim", "source":
		return "PersistentVolumeClaim"
	case "storageclass":
		return "StorageClass"
	case "gateway", "parent":
		return "Gateway"
	}
	return ""
}

func inferredScalarReferenceKind(field string) string {
	switch strings.ToLower(field) {
	case "secretname":
		return "Secret"
	case "configmapname":
		return "ConfigMap"
	case "servicename":
		return "Service"
	case "serviceaccountname":
		return "ServiceAccount"
	case "claimname", "persistentvolumeclaimname":
		return "PersistentVolumeClaim"
	case "storageclassname":
		return "StorageClass"
	}
	return ""
}

func clusterScopedReferenceKind(kind string) bool {
	switch kind {
	case "ClusterRole", "ClusterIssuer", "ClusterSecretStore", "StorageClass", "PersistentVolume", "VolumeSnapshotClass", "ClusterTriggerAuthentication":
		return true
	}
	return false
}

func genericConditionProblems(obj Object) []Problem {
	conditions, _, _ := unstructured.NestedSlice(obj.Raw.Object, "status", "conditions")
	var out []Problem
	for _, condition := range conditions {
		m, ok := condition.(map[string]any)
		if !ok {
			continue
		}
		typ, _, _ := unstructured.NestedString(m, "type")
		status, _, _ := unstructured.NestedString(m, "status")
		bad := (status == "False" && (typ == "Ready" || typ == "Healthy" || typ == "Available" || typ == "Synced" || typ == "Established")) ||
			(status == "True" && (typ == "Degraded" || typ == "Failed" || typ == "Error" || typ == "Stalled"))
		if !bad {
			continue
		}
		reason, _, _ := unstructured.NestedString(m, "reason")
		message, _, _ := unstructured.NestedString(m, "message")
		out = append(out, Problem{Object: obj.Ref, Level: "Warning", Message: conditionMessage(typ, status, reason, message)})
	}
	return out
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
	var restarts int64
	statuses, _, _ := unstructured.NestedSlice(pod.Raw.Object, "status", "containerStatuses")
	for _, status := range statuses {
		if container, ok := status.(map[string]any); ok {
			count, _, _ := unstructured.NestedInt64(container, "restartCount")
			restarts += count
		}
	}
	if restarts >= 5 {
		problems = append(problems, Problem{Object: pod.Ref, Level: "Warning", Message: fmt.Sprintf("Pod containers restarted %d times.", restarts)})
	}
	return problems
}

func quotaProblems(quota Object) []Problem {
	used, _, _ := unstructured.NestedStringMap(quota.Raw.Object, "status", "used")
	hard, _, _ := unstructured.NestedStringMap(quota.Raw.Object, "status", "hard")
	var problems []Problem
	for key, hardText := range hard {
		usedText := used[key]
		usedQuantity, usedErr := resource.ParseQuantity(usedText)
		hardQuantity, hardErr := resource.ParseQuantity(hardText)
		if usedErr != nil || hardErr != nil || hardQuantity.IsZero() {
			continue
		}
		ratio := usedQuantity.AsApproximateFloat64() / hardQuantity.AsApproximateFloat64()
		if ratio >= 0.9 {
			problems = append(problems, Problem{Object: quota.Ref, Level: "Warning", Message: fmt.Sprintf("ResourceQuota %s is %.0f%% used (%s/%s).", key, ratio*100, usedText, hardText)})
		}
	}
	return problems
}

func certificateExpiryProblems(cert Object) []Problem {
	expires, _, _ := unstructured.NestedString(cert.Raw.Object, "status", "notAfter")
	if expires == "" {
		return nil
	}
	deadline, err := time.Parse(time.RFC3339, expires)
	if err != nil {
		return nil
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return []Problem{{Object: cert.Ref, Level: "Broken", Message: "Certificate expired " + deadline.Format(time.RFC3339) + "."}}
	}
	if remaining <= 14*24*time.Hour {
		return []Problem{{Object: cert.Ref, Level: "Warning", Message: fmt.Sprintf("Certificate expires in %d days (%s).", int(remaining.Hours()/24), deadline.Format("2006-01-02"))}}
	}
	return nil
}

func applicationProblems(app Object) []Problem {
	syncStatus, _, _ := unstructured.NestedString(app.Raw.Object, "status", "sync", "status")
	health, _, _ := unstructured.NestedString(app.Raw.Object, "status", "health", "status")
	var out []Problem
	if syncStatus != "" && syncStatus != "Synced" {
		out = append(out, Problem{Object: app.Ref, Level: "Warning", Message: "ArgoCD sync status is " + syncStatus + "."})
	}
	if health == "Degraded" || health == "Missing" || health == "Unknown" {
		out = append(out, Problem{Object: app.Ref, Level: "Broken", Message: "ArgoCD health status is " + health + "."})
	}
	return out
}

func genericPhaseProblems(obj Object) []Problem {
	phase, _, _ := unstructured.NestedString(obj.Raw.Object, "status", "phase")
	switch strings.ToLower(phase) {
	case "failed", "error", "degraded", "partiallyfailed":
		return []Problem{{Object: obj.Ref, Level: "Broken", Message: obj.Ref.Kind + " phase is " + phase + "."}}
	}
	return nil
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

	imagePullSecrets, _, _ := unstructured.NestedSlice(pod.Raw.Object, "spec", "imagePullSecrets")
	for _, rawRef := range imagePullSecrets {
		ref, ok := rawRef.(map[string]any)
		if !ok {
			continue
		}
		if name, _, _ := unstructured.NestedString(ref, "name"); name != "" {
			edges, problems = appendNamedRef(edges, problems, pod, index, "Secret", name, "UsesImagePullSecret", "spec.imagePullSecrets.name", "Pod uses Secret as an image pull credential.")
		}
	}

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
		projected, _, _ := unstructured.NestedSlice(v, "projected", "sources")
		for _, rawSource := range projected {
			source, ok := rawSource.(map[string]any)
			if !ok {
				continue
			}
			if name, _, _ := unstructured.NestedString(source, "configMap", "name"); name != "" {
				edges, problems = appendNamedRef(edges, problems, pod, index, "ConfigMap", name, "Mounts", "spec.volumes.projected.sources.configMap.name", "Pod projected volume mounts ConfigMap.")
			}
			if name, _, _ := unstructured.NestedString(source, "secret", "name"); name != "" {
				edges, problems = appendNamedRef(edges, problems, pod, index, "Secret", name, "Mounts", "spec.volumes.projected.sources.secret.name", "Pod projected volume mounts Secret.")
			}
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
	if name, _, _ := unstructured.NestedString(sub.Raw.Object, "spec", "source"); name != "" {
		namespace, _, _ := unstructured.NestedString(sub.Raw.Object, "spec", "sourceNamespace")
		if namespace == "" {
			namespace = sub.Ref.Namespace
		}
		edges = appendExistingRef(edges, sub, index, ObjectRef{Kind: "CatalogSource", Namespace: namespace, Name: name}, "UsesCatalog", "spec.source", "Subscription resolves packages from CatalogSource.")
	}
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
const argoTrackingIDAnnotation = "argocd.argoproj.io/tracking-id"

func argoApplicationName(obj Object) string {
	if app := obj.Raw.GetLabels()[argoInstanceLabel]; app != "" {
		return app
	}
	trackingID := obj.Raw.GetAnnotations()[argoTrackingIDAnnotation]
	if trackingID == "" {
		return ""
	}
	name, _, _ := strings.Cut(trackingID, ":")
	return name
}

// appendArgoApplicationEdges links an ArgoCD Application to every
// same-namespace object carrying its argocd.argoproj.io/instance label —
// that label is how ArgoCD itself tracks ownership, there's no ownerRef or
// spec field to read. No problem is raised when nothing matches (a fresh
// Application that hasn't synced yet, or whose managed objects live in a
// namespace this snapshot didn't load).
func appendArgoApplicationEdges(edges []Edge, app Object, objects []Object) []Edge {
	destinationNamespace, _, _ := unstructured.NestedString(app.Raw.Object, "spec", "destination", "namespace")
	for _, obj := range objects {
		if destinationNamespace != "" && obj.Ref.Namespace != destinationNamespace {
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

func dedupeProblems(problems []Problem) []Problem {
	indexes := map[string]int{}
	out := make([]Problem, 0, len(problems))
	for _, problem := range problems {
		key := problem.Object.Key() + "|" + problem.Message
		if index, seen := indexes[key]; seen {
			if problemSeverity(problem.Level) > problemSeverity(out[index].Level) {
				out[index] = problem
			}
			continue
		}
		indexes[key] = len(out)
		out = append(out, problem)
	}
	return out
}

func problemSeverity(level string) int {
	switch level {
	case "Broken":
		return 2
	case "Warning":
		return 1
	default:
		return 0
	}
}
