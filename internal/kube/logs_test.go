package kube

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStreamLogsFollowsAndEmitsLines(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces/app/pods/api/log" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("follow") != "true" {
			t.Errorf("expected follow=true, query=%s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("timestamps") != "true" {
			t.Errorf("expected timestamps=true, query=%s", r.URL.RawQuery)
		}
		fmt.Fprintln(w, "2026-08-26T20:00:00Z first")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		fmt.Fprintln(w, "2026-08-26T20:00:01Z second")
	}))
	defer server.Close()

	kubeconfig := filepath.Join(t.TempDir(), "config")
	config := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: %s
contexts:
- name: test
  context:
    cluster: test
    user: test
current-context: test
users:
- name: test
  user: {}
`, server.URL)
	if err := os.WriteFile(kubeconfig, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream := StreamLogs(ctx, LogRequest{
		Options:    Options{Kubeconfig: kubeconfig, ContextName: "test"},
		Namespace:  "app",
		Pods:       map[string][]string{"api": {"main"}},
		TailLines:  20,
		Timestamps: true,
	})
	defer stream.Cancel()

	var lines []string
	for event := range stream.Events {
		if event.Err != nil {
			t.Fatalf("unexpected stream error: %v", event.Err)
		}
		lines = append(lines, event.Line)
	}
	if got := strings.Join(lines, "|"); got != "2026-08-26T20:00:00Z first|2026-08-26T20:00:01Z second" {
		t.Fatalf("unexpected streamed lines: %q", got)
	}
}
