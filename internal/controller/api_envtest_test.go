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

package controller

import (
	"context"
	"fmt"
	"go/build"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	kflaredv1alpha1 "github.com/kode-blox/kflared/api/v1alpha1"
)

func TestCRDDefaultsAndImmutableOwnershipFields(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("controller-runtime envtest cannot terminate control-plane processes on Windows; CI runs this test on Linux")
	}
	assets, err := envtestAssetsDirectory()
	if err != nil {
		t.Fatal(err)
	}
	environment := &envtest.Environment{
		BinaryAssetsDirectory: assets,
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			gatewayAPICRDDirectory(t),
		},
		ErrorIfCRDPathMissing: true,
	}
	config, err := environment.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})

	scheme := bindingTestScheme(t)
	kubeClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	provider := readyProvider()
	provider.Status = kflaredv1alpha1.CloudflareProviderStatus{}
	if err := kubeClient.Create(ctx, provider); err != nil {
		t.Fatalf("create provider: %v", err)
	}
	provider.Spec.AccountID = strings.Repeat("f", 32)
	if err := kubeClient.Update(ctx, provider); err == nil {
		t.Fatal("accountID mutation unexpectedly passed CRD validation")
	}

	binding := &kflaredv1alpha1.CloudflareTunnelBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "defaults", Namespace: "default"},
		Spec: kflaredv1alpha1.CloudflareTunnelBindingSpec{
			ProviderRef:       kflaredv1alpha1.LocalReference{Name: "default"},
			GatewayRef:        kflaredv1alpha1.GatewayReference{Name: "gateway", SectionName: "http"},
			GatewayServiceRef: kflaredv1alpha1.GatewayServiceReference{Name: "traefik", Port: intstr.FromInt32(80)},
		},
	}
	if err := kubeClient.Create(ctx, binding); err != nil {
		t.Fatalf("create binding: %v", err)
	}
	actual := &kflaredv1alpha1.CloudflareTunnelBinding{}
	if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(binding), actual); err != nil {
		t.Fatal(err)
	}
	if actual.Spec.ConnectorReplicas != 2 || actual.Spec.DeletionPolicy != kflaredv1alpha1.DeletionPolicyDelete {
		t.Fatalf("defaults replicas=%d deletionPolicy=%q", actual.Spec.ConnectorReplicas, actual.Spec.DeletionPolicy)
	}
	actual.Spec.GatewayRef.SectionName = "other"
	if err := kubeClient.Update(ctx, actual); err == nil {
		t.Fatal("gatewayRef mutation unexpectedly passed CRD validation")
	}
}

func gatewayAPICRDDirectory(t *testing.T) string {
	t.Helper()
	const version = "v1.6.1"
	for _, goPath := range filepath.SplitList(build.Default.GOPATH) {
		candidate := filepath.Join(goPath, "pkg", "mod", "sigs.k8s.io", "gateway-api@"+version, "config", "crd", "standard")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	t.Fatalf("locate Gateway API %s CRDs in GOPATH", version)
	return ""
}

func envtestAssetsDirectory() (string, error) {
	if configured := os.Getenv("KUBEBUILDER_ASSETS"); configured != "" {
		return configured, nil
	}
	root := filepath.Join("..", "..", "bin", "k8s")
	var result string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.EqualFold(entry.Name(), "kube-apiserver.exe") {
			result = filepath.Dir(path)
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("find envtest assets: %w", err)
	}
	if result == "" {
		return "", fmt.Errorf("envtest assets not found under %s; run the pinned setup-envtest tool first", root)
	}
	return result, nil
}
