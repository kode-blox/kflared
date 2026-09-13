/*
Copyright 2026 Sayak Mukhopadhyay.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package planner

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kflaredv1alpha1 "github.com/kode-blox/kflared/api/v1alpha1"
)

func TestNormalizeHostnames(t *testing.T) {
	allowed, rejected := NormalizeHostnames(
		[]string{"B.Example.com.", "a.example.com", "*.example.com", "bad.other.net", "a.example.com"},
		[]string{"example.com"},
	)
	if got, want := join(allowed), "a.example.com,b.example.com"; got != want {
		t.Fatalf("allowed = %q, want %q", got, want)
	}
	if got, want := join(rejected), "*.example.com,bad.other.net"; got != want {
		t.Fatalf("rejected = %q, want %q", got, want)
	}
}

func TestTunnelNameContainsFullBindingUID(t *testing.T) {
	binding := &kflaredv1alpha1.CloudflareTunnelBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", UID: types.UID("11111111-2222-3333-4444-555555555555")},
		Spec:       kflaredv1alpha1.CloudflareTunnelBindingSpec{GatewayRef: kflaredv1alpha1.GatewayReference{Name: "gateway"}},
	}
	name := TunnelName(types.UID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"), binding)
	if len(name) > maxTunnelNameLength {
		t.Fatalf("name length = %d", len(name))
	}
	if want := "11111111-2222-3333-4444-555555555555"; !contains(name, want) {
		t.Fatalf("name %q does not contain full UID %q", name, want)
	}
}

func join(values []string) string {
	return strings.Join(values, ",")
}

func contains(value, fragment string) bool {
	return strings.Contains(value, fragment)
}
