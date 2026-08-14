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
	Kind      string
	Namespace string
	Name      string
	UID       types.UID
}

func (r ObjectRef) Key() string {
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
	data, err := yaml.Marshal(o.Raw.Object)
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
	return g
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
