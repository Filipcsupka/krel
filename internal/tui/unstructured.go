package tui

import (
	"github.com/filipcsupka/krel/internal/graph"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func unstructuredNestedString(obj graph.Object, fields ...string) (string, bool, error) {
	return unstructured.NestedString(obj.Raw.Object, fields...)
}

func unstructuredNestedStringMap(obj graph.Object, fields ...string) (map[string]string, bool, error) {
	return unstructured.NestedStringMap(obj.Raw.Object, fields...)
}

func unstructuredNestedInt64(obj graph.Object, fields ...string) (int64, bool, error) {
	return unstructured.NestedInt64(obj.Raw.Object, fields...)
}

func unstructuredNestedBool(obj graph.Object, fields ...string) (bool, bool, error) {
	return unstructured.NestedBool(obj.Raw.Object, fields...)
}

func unstructuredNestedSlice(obj graph.Object, fields ...string) ([]any, bool, error) {
	return unstructured.NestedSlice(obj.Raw.Object, fields...)
}

func nestedString(obj map[string]any, fields ...string) (string, bool, error) {
	return unstructured.NestedString(obj, fields...)
}

func nestedInt64(obj map[string]any, fields ...string) (int64, bool, error) {
	return unstructured.NestedInt64(obj, fields...)
}

func nestedSlice(obj map[string]any, fields ...string) ([]any, bool, error) {
	return unstructured.NestedSlice(obj, fields...)
}
