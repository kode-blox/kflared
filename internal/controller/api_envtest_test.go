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
	"encoding/json"
	"fmt"
	"go/build"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	kflaredv1alpha1 "github.com/kode-blox/kflared/api/v1alpha1"
)

func TestControllerClassBackfillAfterCRDUpgrade(t *testing.T) {
	if runtime.GOOS == testWindowsOS {
		t.Skip("controller-runtime envtest cannot terminate control-plane processes on Windows; CI runs this test on Linux")
	}
	assets, err := envtestAssetsDirectory()
	if err != nil {
		t.Fatal(err)
	}

	providerCRD := readCRD(t, filepath.Join("..", "..", "config", "crd", "bases", "kflared.kodeblox.com_clustercloudflareproviders.yaml"))
	bindingCRD := readCRD(t, filepath.Join("..", "..", "config", "crd", "bases", "kflared.kodeblox.com_cloudflaretunnelbindings.yaml"))
	legacyCRDDirectory := t.TempDir()
	writeLegacyCRD(t, legacyCRDDirectory, providerCRD)
	writeLegacyCRD(t, legacyCRDDirectory, bindingCRD)

	environment := &envtest.Environment{
		BinaryAssetsDirectory: assets,
		CRDDirectoryPaths:     []string{legacyCRDDirectory},
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

	// These objects represent resources that were persisted before spec.controller
	// existed in the CRD. The legacy schemas deliberately omit that property.
	legacyProvider := readyClusterProvider()
	legacyProvider.Name = "legacy-provider"
	createWithoutController(t, ctx, kubeClient, legacyProvider)
	legacyBinding := &kflaredv1alpha1.CloudflareTunnelBinding{}
	legacyBinding.Name = "legacy-binding"
	legacyBinding.Namespace = testDefaultName
	legacyBinding.Spec = kflaredv1alpha1.CloudflareTunnelBindingSpec{
		ProviderRef:      kflaredv1alpha1.LocalReference{Name: legacyProvider.Name},
		GatewayRef:       kflaredv1alpha1.GatewayReference{Name: testGatewayResourceName, SectionName: testHTTPSectionName},
		OriginServiceRef: kflaredv1alpha1.OriginServiceReference{Name: testGatewayName, Port: intstr.FromInt32(80)},
	}
	createWithoutController(t, ctx, kubeClient, legacyBinding)

	crdClient, err := apiextensionsclient.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	upgradeCRD(t, ctx, crdClient, providerCRD)
	upgradeCRD(t, ctx, crdClient, bindingCRD)

	backfillControllerClass(t, ctx, kubeClient, legacyProvider, testControllerClass,
		func(object client.Object, value string) {
			object.(*kflaredv1alpha1.ClusterCloudflareProvider).Spec.Controller = value
		},
		func(object client.Object) string {
			return object.(*kflaredv1alpha1.ClusterCloudflareProvider).Spec.Controller
		},
	)
	storedProvider := &kflaredv1alpha1.ClusterCloudflareProvider{}
	if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(legacyProvider), storedProvider); err != nil {
		t.Fatal(err)
	}
	storedProvider.Spec.Controller = testOtherControllerClass
	if err := kubeClient.Update(ctx, storedProvider); err == nil {
		t.Fatal("provider controller mutation after backfill unexpectedly passed CRD validation")
	}

	backfillControllerClass(t, ctx, kubeClient, legacyBinding, testControllerClass,
		func(object client.Object, value string) {
			object.(*kflaredv1alpha1.CloudflareTunnelBinding).Spec.Controller = value
		},
		func(object client.Object) string {
			return object.(*kflaredv1alpha1.CloudflareTunnelBinding).Spec.Controller
		},
	)
	storedBinding := &kflaredv1alpha1.CloudflareTunnelBinding{}
	if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(legacyBinding), storedBinding); err != nil {
		t.Fatal(err)
	}
	storedBinding.Spec.Controller = testOtherControllerClass
	if err := kubeClient.Update(ctx, storedBinding); err == nil {
		t.Fatal("binding controller mutation after backfill unexpectedly passed CRD validation")
	}
}

func TestOriginServiceReferenceMigrationAfterCRDUpgrade(t *testing.T) {
	if runtime.GOOS == testWindowsOS {
		t.Skip("controller-runtime envtest cannot terminate control-plane processes on Windows; CI runs this test on Linux")
	}
	assets, err := envtestAssetsDirectory()
	if err != nil {
		t.Fatal(err)
	}

	bindingCRD := readCRD(t, filepath.Join("..", "..", "config", "crd", "bases", "kflared.kodeblox.com_cloudflaretunnelbindings.yaml"))
	legacyCRDDirectory := t.TempDir()
	writeLegacyOriginReferenceCRD(t, legacyCRDDirectory, bindingCRD)
	environment := &envtest.Environment{
		BinaryAssetsDirectory: assets,
		CRDDirectoryPaths:     []string{legacyCRDDirectory},
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
	legacy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": kflaredv1alpha1.GroupVersion.String(),
		"kind":       "CloudflareTunnelBinding",
		"metadata": map[string]any{
			testNameField: "legacy-origin",
			"namespace":   testDefaultName,
		},
		"spec": map[string]any{
			"controller":  testControllerClass,
			"providerRef": map[string]any{testNameField: testDefaultName},
			"gatewayRef": map[string]any{
				testNameField: testGatewayResourceName,
				"sectionName": testHTTPSectionName,
			},
			"gatewayServiceRef": map[string]any{testNameField: testGatewayName, "port": testOriginPortName},
		},
	}}
	if err := kubeClient.Create(ctx, legacy); err != nil {
		t.Fatalf("create binding with released gatewayServiceRef schema: %v", err)
	}

	crdClient, err := apiextensionsclient.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	upgradeCRD(t, ctx, crdClient, bindingCRD)

	key := client.ObjectKeyFromObject(legacy)
	var lastErr error
	err = wait.PollUntilContextTimeout(ctx, 10*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
		candidate := &unstructured.Unstructured{}
		candidate.SetAPIVersion(kflaredv1alpha1.GroupVersion.String())
		candidate.SetKind("CloudflareTunnelBinding")
		if err := kubeClient.Get(ctx, key, candidate); err != nil {
			return false, err
		}
		if err := unstructured.SetNestedMap(candidate.Object, map[string]any{testNameField: testGatewayName, "namespace": testSharedOriginNamespace, "port": testOriginPortName}, "spec", "originServiceRef"); err != nil {
			return false, err
		}
		unstructured.RemoveNestedField(candidate.Object, "spec", "gatewayServiceRef")
		if err := kubeClient.Update(ctx, candidate); err != nil {
			lastErr = err
			if apierrors.IsConflict(err) || apierrors.IsInvalid(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("rewrite stored binding to originServiceRef after CRD upgrade: %v (last update error: %v)", err, lastErr)
	}

	stored := &kflaredv1alpha1.CloudflareTunnelBinding{}
	if err := kubeClient.Get(ctx, key, stored); err != nil {
		t.Fatal(err)
	}
	if stored.Spec.OriginServiceRef.Name != testGatewayName || stored.Spec.OriginServiceRef.Namespace != testSharedOriginNamespace || stored.Spec.OriginServiceRef.Port.String() != testOriginPortName {
		t.Fatalf("migrated originServiceRef = %#v", stored.Spec.OriginServiceRef)
	}
}

func readCRD(t *testing.T, path string) *apiextensionsv1.CustomResourceDefinition {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	jsonContent, err := utilyaml.ToJSON(content)
	if err != nil {
		t.Fatal(err)
	}
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := json.Unmarshal(jsonContent, crd); err != nil {
		t.Fatal(err)
	}
	return crd
}

func writeLegacyCRD(t *testing.T, directory string, current *apiextensionsv1.CustomResourceDefinition) {
	t.Helper()
	legacy := current.DeepCopy()
	schema := legacy.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
	delete(schema.Properties, "controller")
	filteredRequired := schema.Required[:0]
	for _, field := range schema.Required {
		if field != "controller" {
			filteredRequired = append(filteredRequired, field)
		}
	}
	schema.Required = filteredRequired
	legacy.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"] = schema
	content, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, legacy.Name+".json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeLegacyOriginReferenceCRD(t *testing.T, directory string, current *apiextensionsv1.CustomResourceDefinition) {
	t.Helper()
	legacy := current.DeepCopy()
	schema := legacy.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
	originSchema := schema.Properties["originServiceRef"]
	delete(schema.Properties, "originServiceRef")
	schema.Properties["gatewayServiceRef"] = originSchema
	for i, field := range schema.Required {
		if field == "originServiceRef" {
			schema.Required[i] = "gatewayServiceRef"
		}
	}
	legacy.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"] = schema
	content, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, legacy.Name+".json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func createWithoutController(t *testing.T, ctx context.Context, kubeClient client.Client, object client.Object) {
	t.Helper()
	content, err := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(object)
	if err != nil {
		t.Fatal(err)
	}
	unstructured.RemoveNestedField(content, "spec", "controller")
	legacy := &unstructured.Unstructured{Object: content}
	legacy.SetAPIVersion(kflaredv1alpha1.GroupVersion.String())
	switch object.(type) {
	case *kflaredv1alpha1.ClusterCloudflareProvider:
		legacy.SetKind("ClusterCloudflareProvider")
	case *kflaredv1alpha1.CloudflareTunnelBinding:
		legacy.SetKind("CloudflareTunnelBinding")
	default:
		t.Fatalf("unsupported legacy object type %T", object)
	}
	if err := kubeClient.Create(ctx, legacy); err != nil {
		t.Fatalf("create legacy %s: %v", legacy.GetKind(), err)
	}
}

func upgradeCRD(t *testing.T, ctx context.Context, crdClient *apiextensionsclient.Clientset, current *apiextensionsv1.CustomResourceDefinition) {
	t.Helper()
	stored, err := crdClient.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, current.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	stored.Spec = current.Spec
	if _, err := crdClient.ApiextensionsV1().CustomResourceDefinitions().Update(ctx, stored, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("upgrade CRD %s: %v", current.Name, err)
	}
}

func backfillControllerClass(
	t *testing.T,
	ctx context.Context,
	kubeClient client.Client,
	object client.Object,
	expected string,
	setController func(client.Object, string),
	getController func(client.Object) string,
) {
	t.Helper()
	key := client.ObjectKeyFromObject(object)
	var lastErr error
	err := wait.PollUntilContextTimeout(ctx, 10*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
		candidate := object.DeepCopyObject().(client.Object)
		if err := kubeClient.Get(ctx, key, candidate); err != nil {
			return false, fmt.Errorf("get legacy object: %w", err)
		}
		setController(candidate, expected)
		if err := kubeClient.Update(ctx, candidate); err != nil {
			lastErr = fmt.Errorf("update controller class: %w", err)
			if apierrors.IsConflict(err) {
				return false, nil
			}
			return false, lastErr
		}

		stored := object.DeepCopyObject().(client.Object)
		if err := kubeClient.Get(ctx, key, stored); err != nil {
			return false, fmt.Errorf("verify updated object: %w", err)
		}
		if actual := getController(stored); actual != expected {
			lastErr = fmt.Errorf("stored controller class is %q, want %q", actual, expected)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		if lastErr != nil {
			t.Fatalf("backfill controller class for %s: %v (poll failed: %v)", key, lastErr, err)
		}
		t.Fatalf("backfill controller class for %s: %v", key, err)
	}
}

func TestCRDDefaultsAndImmutableOwnershipFields(t *testing.T) {
	if runtime.GOOS == testWindowsOS {
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
	provider := readyClusterProvider()
	provider.Status = kflaredv1alpha1.ClusterCloudflareProviderStatus{}
	if err := kubeClient.Create(ctx, provider); err != nil {
		t.Fatalf("create provider: %v", err)
	}
	provider.Spec.AccountID = strings.Repeat("f", 32)
	if err := kubeClient.Update(ctx, provider); err == nil {
		t.Fatal("accountID mutation unexpectedly passed CRD validation")
	}
	storedProvider := &kflaredv1alpha1.ClusterCloudflareProvider{}
	if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(provider), storedProvider); err != nil {
		t.Fatal(err)
	}
	storedProvider.Spec.Controller = testOtherControllerClass
	if err := kubeClient.Update(ctx, storedProvider); err == nil {
		t.Fatal("controller class mutation unexpectedly passed CRD validation")
	}
	emptyProvider := readyClusterProvider()
	emptyProvider.Name = "empty-controller"
	emptyProvider.Spec.Controller = ""
	if err := kubeClient.Create(ctx, emptyProvider); err == nil {
		t.Fatal("empty provider controller class unexpectedly passed CRD validation")
	}
	tooManyReplicas := &kflaredv1alpha1.CloudflareTunnelBinding{
		Name: "too-many-replicas", Namespace: testDefaultName,
		Spec: kflaredv1alpha1.CloudflareTunnelBindingSpec{
			Controller:        testControllerClass,
			ProviderRef:       kflaredv1alpha1.LocalReference{Name: testDefaultName},
			GatewayRef:        kflaredv1alpha1.GatewayReference{Name: testGatewayResourceName, SectionName: testHTTPSectionName},
			OriginServiceRef:  kflaredv1alpha1.OriginServiceReference{Name: testGatewayName, Port: intstr.FromInt32(80)},
			ConnectorReplicas: 11,
		},
	}
	if err := kubeClient.Create(ctx, tooManyReplicas); err == nil {
		t.Fatal("connectorReplicas above ten unexpectedly passed CRD validation")
	}
	invalidOriginNamespace := tooManyReplicas.DeepCopy()
	invalidOriginNamespace.Name = "invalid-origin-namespace"
	invalidOriginNamespace.Spec.ConnectorReplicas = 2
	invalidOriginNamespace.Spec.OriginServiceRef.Namespace = "Not-A-Namespace"
	if err := kubeClient.Create(ctx, invalidOriginNamespace); err == nil {
		t.Fatal("invalid origin Service namespace unexpectedly passed CRD validation")
	}
	tooManyReplicas.Spec.Controller = ""
	tooManyReplicas.Spec.ConnectorReplicas = 2
	if err := kubeClient.Create(ctx, tooManyReplicas); err == nil {
		t.Fatal("empty binding controller class unexpectedly passed CRD validation")
	}

	binding := &kflaredv1alpha1.CloudflareTunnelBinding{
		Name: "defaults", Namespace: testDefaultName,
		Spec: kflaredv1alpha1.CloudflareTunnelBindingSpec{
			Controller:       testControllerClass,
			ProviderRef:      kflaredv1alpha1.LocalReference{Name: testDefaultName},
			GatewayRef:       kflaredv1alpha1.GatewayReference{Name: testGatewayResourceName, SectionName: testHTTPSectionName},
			OriginServiceRef: kflaredv1alpha1.OriginServiceReference{Name: testGatewayName, Port: intstr.FromInt32(80)},
		},
	}
	if err := kubeClient.Create(ctx, binding); err != nil {
		t.Fatalf("create binding: %v", err)
	}
	actual := &kflaredv1alpha1.CloudflareTunnelBinding{}
	if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(binding), actual); err != nil {
		t.Fatal(err)
	}
	if actual.Spec.ConnectorReplicas != 2 || actual.Spec.DeletionPolicy != kflaredv1alpha1.DeletionPolicyDelete || actual.Spec.DNSAutomationEnabled == nil || !*actual.Spec.DNSAutomationEnabled {
		t.Fatalf("defaults replicas=%d deletionPolicy=%q dnsAutomationEnabled=%v", actual.Spec.ConnectorReplicas, actual.Spec.DeletionPolicy, actual.Spec.DNSAutomationEnabled)
	}
	dnsDisabled := binding.DeepCopy()
	dnsDisabled.ObjectMeta = metav1.ObjectMeta{Name: "dns-automation-disabled", Namespace: testDefaultName}
	dnsDisabled.Spec.DNSAutomationEnabled = boolPointer(false)
	if err := kubeClient.Create(ctx, dnsDisabled); err != nil {
		t.Fatalf("create binding with DNS automation disabled: %v", err)
	}
	if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(dnsDisabled), dnsDisabled); err != nil {
		t.Fatal(err)
	}
	if dnsDisabled.Spec.DNSAutomationEnabled == nil || *dnsDisabled.Spec.DNSAutomationEnabled {
		t.Fatal("explicit DNS automation opt-out was not preserved")
	}
	actual.Spec.Controller = testOtherControllerClass
	if err := kubeClient.Update(ctx, actual); err == nil {
		t.Fatal("binding controller class mutation unexpectedly passed CRD validation")
	}
	if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(binding), actual); err != nil {
		t.Fatal(err)
	}
	actual.Spec.GatewayRef.SectionName = "other"
	if err := kubeClient.Update(ctx, actual); err == nil {
		t.Fatal("gatewayRef mutation unexpectedly passed CRD validation")
	}

	missingProvider := readyClusterProvider()
	missingProvider.Name = "missing-controller"
	providerObject, err := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(missingProvider)
	if err != nil {
		t.Fatal(err)
	}
	delete(providerObject["spec"].(map[string]any), "controller")
	providerUnstructured := &unstructured.Unstructured{Object: providerObject}
	providerUnstructured.SetAPIVersion(kflaredv1alpha1.GroupVersion.String())
	providerUnstructured.SetKind("ClusterCloudflareProvider")
	if err := kubeClient.Create(ctx, providerUnstructured); err == nil {
		t.Fatal("provider without controller unexpectedly passed CRD validation")
	}

	missingBinding := &kflaredv1alpha1.CloudflareTunnelBinding{}
	missingBinding.Name = "missing-controller"
	missingBinding.Namespace = testDefaultName
	missingBinding.Spec = kflaredv1alpha1.CloudflareTunnelBindingSpec{
		ProviderRef:      kflaredv1alpha1.LocalReference{Name: testDefaultName},
		GatewayRef:       kflaredv1alpha1.GatewayReference{Name: testGatewayResourceName, SectionName: testHTTPSectionName},
		OriginServiceRef: kflaredv1alpha1.OriginServiceReference{Name: testGatewayName, Port: intstr.FromInt32(80)},
	}
	bindingObject, err := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(missingBinding)
	if err != nil {
		t.Fatal(err)
	}
	delete(bindingObject["spec"].(map[string]any), "controller")
	missingBindingUnstructured := &unstructured.Unstructured{Object: bindingObject}
	missingBindingUnstructured.SetAPIVersion(kflaredv1alpha1.GroupVersion.String())
	missingBindingUnstructured.SetKind("CloudflareTunnelBinding")
	if err := kubeClient.Create(ctx, missingBindingUnstructured); err == nil {
		t.Fatal("binding without controller unexpectedly passed CRD validation")
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
