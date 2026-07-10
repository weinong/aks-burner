package kubetarget

import (
	"reflect"
	"testing"
)

func TestKubectlCommandAddsExplicitContext(t *testing.T) {
	got := (Target{Context: "preview"}).KubectlCommand("get", "nodes")
	want := []string{"kubectl", "--context", "preview", "get", "nodes"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("KubectlCommand() = %#v, want %#v", got, want)
	}
}

func TestKubectlCommandPreservesLegacyArguments(t *testing.T) {
	got := (Target{}).KubectlCommand("get", "nodes")
	want := []string{"kubectl", "get", "nodes"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("KubectlCommand() = %#v, want %#v", got, want)
	}
}

func TestKubeBurnerArgsAddsExplicitContext(t *testing.T) {
	got := (Target{Context: "preview"}).KubeBurnerArgs("init", "-c", "workload.yml")
	want := []string{"init", "-c", "workload.yml", "--kube-context", "preview"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("KubeBurnerArgs() = %#v, want %#v", got, want)
	}
}

func TestKubeBurnerArgsPreservesLegacyArguments(t *testing.T) {
	got := (Target{}).KubeBurnerArgs("init", "-c", "workload.yml")
	want := []string{"init", "-c", "workload.yml"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("KubeBurnerArgs() = %#v, want %#v", got, want)
	}
}
