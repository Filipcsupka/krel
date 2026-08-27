package graph

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"
)

type ObjectRef struct {
	Group     string
	Kind      string
	Namespace string
	Name      string
	UID       types.UID
}

func (r ObjectRef) Key() string {
	if r.Group == "" {
		return r.IdentityKey()
	}
	return fmt.Sprintf("%s|%s", r.Group, r.IdentityKey())
}

// IdentityKey is the traditional kind/namespace/name identity used when an
// API reference omits its group. Group-aware Key is required to distinguish
// CRDs that reuse the same Kind (common with Crossplane providers).
func (r ObjectRef) IdentityKey() string {
	return fmt.Sprintf("%s/%s/%s", r.Kind, r.Namespace, r.Name)
}

func (r ObjectRef) Label() string {
	if r.Namespace == "" {
		return fmt.Sprintf("%s/%s", r.Kind, r.Name)
	}
	return fmt.Sprintf("%s/%s", r.Kind, r.Name)
}

type Edge struct {
	From   ObjectRef
	To     ObjectRef
	Type   string
	Reason string
	Health string
	Source string
}

type Problem struct {
	Object  ObjectRef
	Level   string
	Message string
}

type Object struct {
	Ref    ObjectRef
	GVK    string
	Labels map[string]string
	Raw    *unstructured.Unstructured
	Events []string
}

func (o Object) Summary() []string {
	lines := []string{o.Ref.Label()}
	if o.Ref.Namespace != "" {
		lines = append(lines, "namespace: "+o.Ref.Namespace)
	}
	if len(o.Labels) > 0 {
		pairs := make([]string, 0, len(o.Labels))
		for k, v := range o.Labels {
			pairs = append(pairs, k+"="+v)
		}
		sort.Strings(pairs)
		lines = append(lines, "labels: "+strings.Join(pairs, ", "))
	}
	return lines
}

func (o Object) YAML() string {
	object := o.Raw.Object
	if o.Ref.Kind == "Secret" {
		object = o.Raw.DeepCopy().Object
		for _, field := range []string{"data", "stringData"} {
			values, found, _ := unstructured.NestedStringMap(object, field)
			if !found {
				continue
			}
			for key := range values {
				values[key] = "<redacted>"
			}
			_ = unstructured.SetNestedStringMap(object, values, field)
		}
	}
	data, err := yaml.Marshal(object)
	if err != nil {
		return err.Error()
	}
	return string(data)
}

type Graph struct {
	Objects  []Object
	Edges    []Edge
	Problems []Problem

	byKey map[string]int
	byUID map[types.UID]int
}

func New(objects []Object, edges []Edge, problems []Problem) *Graph {
	g := &Graph{
		Objects:  objects,
		Edges:    edges,
		Problems: problems,
		byKey:    map[string]int{},
		byUID:    map[types.UID]int{},
	}
	for i, obj := range objects {
		g.byKey[obj.Ref.Key()] = i
		if obj.Ref.UID != "" {
			g.byUID[obj.Ref.UID] = i
		}
	}
	addUnambiguousAliases(g.byKey, objects)
	return g
}

func addUnambiguousAliases(index map[string]int, objects []Object) {
	ambiguous := map[string]bool{}
	for i, obj := range objects {
		alias := obj.Ref.IdentityKey()
		if existing, ok := index[alias]; ok && existing != i {
			ambiguous[alias] = true
			continue
		}
		index[alias] = i
	}
	for alias := range ambiguous {
		delete(index, alias)
	}
}

func (g *Graph) ObjectByKey(key string) (Object, bool) {
	i, ok := g.byKey[key]
	if !ok {
		return Object{}, false
	}
	return g.Objects[i], true
}

func (g *Graph) ObjectByUID(uid types.UID) (Object, bool) {
	i, ok := g.byUID[uid]
	if !ok {
		return Object{}, false
	}
	return g.Objects[i], true
}

func (g *Graph) EdgesFor(ref ObjectRef) []Edge {
	var out []Edge
	for _, edge := range g.Edges {
		if edge.From.Key() == ref.Key() || edge.To.Key() == ref.Key() {
			out = append(out, edge)
		}
	}
	return out
}

func (g *Graph) ProblemsFor(ref ObjectRef) []Problem {
	var out []Problem
	for _, problem := range g.Problems {
		if problem.Object.Key() == ref.Key() {
			out = append(out, problem)
		}
	}
	return out
}

// ImpactFor walks objects likely to be affected if ref becomes unavailable.
// It understands the graph's two edge conventions: ownership/selection
// edges point from controller to dependent, while reference edges point from
// consumer to dependency.
func (g *Graph) ImpactFor(ref ObjectRef, maxDepth int) []ObjectRef {
	if maxDepth <= 0 {
		maxDepth = 4
	}
	type queued struct {
		ref   ObjectRef
		depth int
	}
	queue := []queued{{ref: ref}}
	seen := map[string]bool{ref.Key(): true}
	var out []ObjectRef
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current.depth >= maxDepth {
			continue
		}
		for _, edge := range g.EdgesFor(current.ref) {
			next, ok := affectedNeighbor(edge, current.ref)
			if !ok || seen[next.Key()] {
				continue
			}
			seen[next.Key()] = true
			out = append(out, next)
			queue = append(queue, queued{ref: next, depth: current.depth + 1})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

func affectedNeighbor(edge Edge, current ObjectRef) (ObjectRef, bool) {
	if edge.Type == "Owns" {
		if edge.From.Key() == current.Key() {
			return edge.To, true
		}
		if edge.To.Key() == current.Key() {
			return edge.From, true
		}
		return ObjectRef{}, false
	}
	controllerToDependent := map[string]bool{
		"Selects": true, "HasEndpoints": true, "Manages": true,
		"Protects": true, "Monitors": true, "BelongsTo": true,
	}
	if controllerToDependent[edge.Type] {
		if edge.From.Key() == current.Key() {
			return edge.To, true
		}
		return ObjectRef{}, false
	}
	if edge.To.Key() == current.Key() {
		return edge.From, true
	}
	return ObjectRef{}, false
}

// ProblemPath finds the shortest useful graph path from ref to an unhealthy
// related object. Workload ownership is followed downward; normal references
// are followed from consumer to dependency.
func (g *Graph) ProblemPath(ref ObjectRef, maxDepth int) ([]ObjectRef, Problem, bool) {
	if maxDepth <= 0 {
		maxDepth = 5
	}
	type queued struct {
		ref  ObjectRef
		path []ObjectRef
	}
	queue := []queued{{ref: ref, path: []ObjectRef{ref}}}
	seen := map[string]bool{ref.Key(): true}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if len(current.path) > 1 {
			if problems := g.ProblemsFor(current.ref); len(problems) > 0 {
				return current.path, problems[0], true
			}
		}
		if len(current.path)-1 >= maxDepth {
			continue
		}
		for _, edge := range g.EdgesFor(current.ref) {
			next, ok := causeNeighbor(edge, current.ref)
			if !ok || seen[next.Key()] {
				continue
			}
			seen[next.Key()] = true
			path := append(append([]ObjectRef{}, current.path...), next)
			queue = append(queue, queued{ref: next, path: path})
		}
	}
	return nil, Problem{}, false
}

func causeNeighbor(edge Edge, current ObjectRef) (ObjectRef, bool) {
	if edge.Type == "Owns" && edge.From.Key() == current.Key() {
		return edge.To, true
	}
	if edge.From.Key() == current.Key() && edge.Type != "Selects" && edge.Type != "HasEndpoints" && edge.Type != "Manages" && edge.Type != "Protects" && edge.Type != "Monitors" {
		return edge.To, true
	}
	return ObjectRef{}, false
}
