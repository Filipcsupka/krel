package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/filipcsupka/krel/internal/graph"
	"github.com/filipcsupka/krel/internal/kube"
	"github.com/filipcsupka/krel/internal/tui"
)

func Run(args []string, stderr io.Writer, binaryName string) int {
	flags := flag.NewFlagSet(binaryName, flag.ContinueOnError)
	flags.SetOutput(stderr)

	var namespace string
	var kubeconfig string
	var contextName string
	var allNamespaces bool

	flags.StringVar(&namespace, "namespace", "", "namespace to inspect")
	flags.StringVar(&namespace, "n", "", "namespace to inspect")
	flags.StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig")
	flags.StringVar(&contextName, "context", "", "kubeconfig context")
	flags.BoolVar(&allNamespaces, "all-namespaces", false, "inspect resources across all namespaces")
	flags.BoolVar(&allNamespaces, "A", false, "inspect resources across all namespaces")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	resourceKind := ""
	positional := flags.Args()
	if len(positional) == 3 && (positional[0] == "why" || positional[0] == "refs") {
		resourceKind = positional[1]
	}
	snapshot, err := kube.LoadSnapshot(context.Background(), kube.Options{
		Namespace:     namespace,
		Kubeconfig:    kubeconfig,
		ContextName:   contextName,
		AllNamespaces: allNamespaces,
		ResourceKind:  resourceKind,
	})
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", binaryName, err)
		return 1
	}
	if handled, err := runCommand(positional, snapshot, binaryName); handled {
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", binaryName, err)
			return 1
		}
		return 0
	}

	program := tea.NewProgram(tui.New(snapshot), tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", binaryName, err)
		return 1
	}
	return 0
}

func runCommand(args []string, snapshot kube.Snapshot, binaryName string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "why":
		if len(args) != 3 {
			return true, fmt.Errorf("usage: %s why <kind> <name>", binaryName)
		}
		obj, ok := findObject(snapshot, args[1], args[2])
		if !ok {
			return true, fmt.Errorf("%s/%s not found in namespace %s", args[1], args[2], snapshot.Namespace)
		}
		printWhy(snapshot.Graph, obj)
		return true, nil
	case "refs":
		if len(args) != 3 {
			return true, fmt.Errorf("usage: %s refs <kind> <name>", binaryName)
		}
		obj, ok := findObject(snapshot, args[1], args[2])
		if !ok {
			return true, fmt.Errorf("%s/%s not found in namespace %s", args[1], args[2], snapshot.Namespace)
		}
		printRefs(snapshot.Graph, obj)
		return true, nil
	case "problems":
		printProblems(snapshot.Graph)
		return true, nil
	default:
		return true, fmt.Errorf("unknown command %q", args[0])
	}
}

func findObject(snapshot kube.Snapshot, kind, name string) (graph.Object, bool) {
	normalized := normalizeKind(kind)
	group := ""
	if resource, ok := snapshot.ResolveResource(kind); ok {
		normalized = resource.Kind
		group = resource.GVR.Group
	}
	for _, obj := range snapshot.Graph.Objects {
		if strings.EqualFold(obj.Ref.Kind, normalized) && obj.Ref.Name == name && (group == "" || obj.Ref.Group == group) {
			return obj, true
		}
	}
	return graph.Object{}, false
}

func normalizeKind(kind string) string {
	switch strings.ToLower(kind) {
	case "po", "pod", "pods":
		return "Pod"
	case "deploy", "deployment", "deployments":
		return "Deployment"
	case "rs", "replicaset", "replicasets":
		return "ReplicaSet"
	case "svc", "service", "services":
		return "Service"
	case "cm", "configmap", "configmaps":
		return "ConfigMap"
	case "secret", "secrets":
		return "Secret"
	case "pvc", "persistentvolumeclaim", "persistentvolumeclaims":
		return "PersistentVolumeClaim"
	case "sa", "serviceaccount", "serviceaccounts":
		return "ServiceAccount"
	case "ing", "ingress", "ingresses":
		return "Ingress"
	case "route", "routes":
		return "Route"
	default:
		return kind
	}
}

func printWhy(g *graph.Graph, obj graph.Object) {
	fmt.Println(strings.Join(obj.Summary(), "\n"))
	fmt.Println()
	printProblemsFor(g, obj.Ref)
	for _, edge := range g.EdgesFor(obj.Ref) {
		fmt.Printf("%s -> %s [%s]\n", edge.From.Label(), edge.To.Label(), edge.Type)
		fmt.Printf("Reason: %s\n", edge.Reason)
		fmt.Printf("Source: %s\n\n", edge.Source)
	}
}

func printRefs(g *graph.Graph, obj graph.Object) {
	fmt.Printf("%s references and consumers\n\n", obj.Ref.Label())
	for _, edge := range g.Edges {
		if edge.From.Key() == obj.Ref.Key() {
			fmt.Printf("references: %s [%s]\n  %s\n", edge.To.Label(), edge.Type, edge.Reason)
		}
		if edge.To.Key() == obj.Ref.Key() {
			fmt.Printf("consumer: %s [%s]\n  %s\n", edge.From.Label(), edge.Type, edge.Reason)
		}
	}
	printProblemsFor(g, obj.Ref)
}

func printProblems(g *graph.Graph) {
	if len(g.Problems) == 0 {
		fmt.Println("No graph-derived problems found.")
		return
	}
	for _, problem := range g.Problems {
		fmt.Printf("[%s] %s: %s\n", problem.Level, problem.Object.Label(), problem.Message)
	}
}

func printProblemsFor(g *graph.Graph, ref graph.ObjectRef) {
	for _, problem := range g.ProblemsFor(ref) {
		fmt.Printf("[%s] %s\n", problem.Level, problem.Message)
	}
}
