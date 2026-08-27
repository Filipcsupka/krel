package kube

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type LogRequest struct {
	Options    Options
	Namespace  string
	Pods       map[string][]string
	TailLines  int64
	Grep       string
	Previous   bool
	Timestamps bool
	Since      time.Duration
	MaxPerPane int
}

type LogResult struct {
	Lines []string
	Err   error
}

// LogEvent is one line (or one non-fatal source error) from a live log
// stream. Source is only rendered when multiple pod/container streams are
// merged, so a single-pod view stays as clean as k9s.
type LogEvent struct {
	Line   string
	Source string
	Err    error
}

// LogStream owns a merged set of pod/container log streams. Cancel must be
// called when the selected resource or log options change.
type LogStream struct {
	Events <-chan LogEvent
	Cancel context.CancelFunc
}

// StreamLogs follows every requested pod/container concurrently and merges
// their lines into one channel. The API server supplies timestamps, making
// the merged tail useful without periodically replacing the entire buffer.
func StreamLogs(parent context.Context, req LogRequest) LogStream {
	ctx, cancel := context.WithCancel(parent)
	events := make(chan LogEvent, 256)
	go func() {
		defer close(events)
		if len(req.Pods) == 0 {
			sendLogEvent(ctx, events, LogEvent{Line: "No pod selected for logs."})
			return
		}

		client, err := logClient(req.Options)
		if err != nil {
			sendLogEvent(ctx, events, LogEvent{Err: err})
			return
		}

		namespace := req.Namespace
		if namespace == "" {
			namespace = req.Options.Namespace
		}
		tail := req.TailLines
		if tail < 0 {
			tail = 160
		}

		type source struct{ pod, container string }
		var sources []source
		for pod, containers := range req.Pods {
			for _, container := range containers {
				sources = append(sources, source{pod: pod, container: container})
			}
		}
		sort.Slice(sources, func(i, j int) bool {
			if sources[i].pod == sources[j].pod {
				return sources[i].container < sources[j].container
			}
			return sources[i].pod < sources[j].pod
		})

		var wg sync.WaitGroup
		for _, src := range sources {
			wg.Add(1)
			go func() {
				defer wg.Done()
				streamContainerLogs(ctx, client, namespace, src.pod, src.container, tail, req, len(sources) > 1, events)
			}()
		}
		wg.Wait()
	}()
	return LogStream{Events: events, Cancel: cancel}
}

func logClient(opts Options) (*kubernetes.Clientset, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.Kubeconfig != "" {
		loadingRules.ExplicitPath = opts.Kubeconfig
	}
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		&clientcmd.ConfigOverrides{CurrentContext: opts.ContextName},
	)
	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(restConfig)
}

func streamContainerLogs(ctx context.Context, client *kubernetes.Clientset, namespace, pod, container string, tail int64, req LogRequest, showSource bool, out chan<- LogEvent) {
	opts := &corev1.PodLogOptions{
		Container:  container,
		TailLines:  &tail,
		Timestamps: req.Timestamps,
		Follow:     !req.Previous,
		Previous:   req.Previous,
	}
	if req.Since > 0 {
		seconds := int64(req.Since.Seconds())
		opts.SinceSeconds = &seconds
	}
	stream, err := client.CoreV1().Pods(namespace).GetLogs(pod, opts).Stream(ctx)
	if err != nil {
		sendLogEvent(ctx, out, LogEvent{Source: pod + "/" + container, Err: err})
		return
	}
	defer stream.Close()

	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if req.Grep != "" && !strings.Contains(strings.ToLower(line), strings.ToLower(req.Grep)) {
			continue
		}
		source := ""
		if showSource {
			source = pod + "/" + container
		}
		if !sendLogEvent(ctx, out, LogEvent{Line: line, Source: source}) {
			return
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		sendLogEvent(ctx, out, LogEvent{Source: pod + "/" + container, Err: err})
	}
}

func sendLogEvent(ctx context.Context, out chan<- LogEvent, event LogEvent) bool {
	select {
	case out <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func LoadLogs(ctx context.Context, req LogRequest) LogResult {
	if len(req.Pods) == 0 {
		return LogResult{Lines: []string{"No pod selected for logs."}}
	}
	client, err := logClient(req.Options)
	if err != nil {
		return LogResult{Err: err}
	}

	tail := req.TailLines
	if tail == 0 {
		tail = 120
	}
	maxLines := req.MaxPerPane
	if maxLines == 0 {
		maxLines = 300
	}
	namespace := req.Namespace
	if namespace == "" {
		namespace = req.Options.Namespace
	}

	lines := []string{}
	for podName, containers := range req.Pods {
		for _, container := range containers {
			podLines, err := loadContainerLogs(ctx, client, namespace, podName, container, tail, req.Previous, req.Timestamps, req.Since)
			if err != nil {
				lines = append(lines, fmt.Sprintf("[%s/%s] %v", podName, container, err))
				continue
			}
			for _, line := range podLines {
				if req.Grep != "" && !strings.Contains(strings.ToLower(line), strings.ToLower(req.Grep)) {
					continue
				}
				lines = append(lines, line)
			}
		}
	}
	if len(lines) == 0 {
		if req.Grep != "" {
			return LogResult{Lines: []string{"No log lines match grep: " + req.Grep}}
		}
		return LogResult{Lines: []string{"No logs returned."}}
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return LogResult{Lines: lines}
}

func loadContainerLogs(ctx context.Context, client *kubernetes.Clientset, namespace, podName, container string, tail int64, previous, timestamps bool, since time.Duration) ([]string, error) {
	timeout := int64((10 * time.Second).Seconds())
	opts := &corev1.PodLogOptions{
		Container:  container,
		TailLines:  &tail,
		Timestamps: timestamps,
		Follow:     false,
		Previous:   previous,
	}
	if since > 0 {
		seconds := int64(since.Seconds())
		opts.SinceSeconds = &seconds
	}
	stream, err := client.CoreV1().Pods(namespace).GetLogs(podName, opts).Timeout(time.Duration(timeout) * time.Second).Stream(ctx)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	data, err := io.ReadAll(stream)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, "\n"), nil
}
