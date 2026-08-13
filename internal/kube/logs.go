package kube

import (
	"context"
	"fmt"
	"io"
	"strings"
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

func LoadLogs(ctx context.Context, req LogRequest) LogResult {
	if len(req.Pods) == 0 {
		return LogResult{Lines: []string{"No pod selected for logs."}}
	}
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if req.Options.Kubeconfig != "" {
		loadingRules.ExplicitPath = req.Options.Kubeconfig
	}
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		&clientcmd.ConfigOverrides{CurrentContext: req.Options.ContextName},
	)
	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return LogResult{Err: err}
	}
	client, err := kubernetes.NewForConfig(restConfig)
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
				lines = append(lines, fmt.Sprintf("%s/%s %s", podName, container, line))
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
