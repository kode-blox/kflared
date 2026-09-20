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
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kflaredv1alpha1 "github.com/kode-blox/kflared/api/v1alpha1"
	cfclient "github.com/kode-blox/kflared/internal/cloudflare"
)

const (
	testControllerClass                = "test-class"
	testDefaultName                    = "default"
	testGatewayResourceName            = "gateway"
	testOtherControllerClass           = "other-class"
	testTenantName                     = "tenant"
	testGatewayName                    = "traefik"
	testSharedOriginNamespace          = testGatewayName
	testOriginPortName                 = "web"
	testNameField                      = "name"
	testHTTPSectionName                = "http"
	testAPITokenSecretName             = "cloudflare-api-token"
	testAPITokenSecretKey              = "api-token"
	testConnectorToken                 = "connector-token"
	testApplicationHostname            = "app.example.com"
	testTunnelID                       = "tunnel-id"
	testOldOrigin                      = "http://old"
	testNotFoundOrigin                 = "http_status:404"
	testInvalidServiceReason           = "InvalidService"
	testTunnelReconciliationReason     = "TunnelReconciliationFailed"
	testTunnelReconciliationMessage    = "Tunnel reconciliation could not complete"
	testConnectorReconciliationReason  = "ConnectorReconciliationFailed"
	testConnectorReconciliationMessage = "Connector reconciliation could not complete"
	testManagedResourceName            = "kflared-111111112222"
	testBindingTunnelName              = "binding-tunnel"
	testWindowsOS                      = "windows"
)

type fakeCloudflareFactory struct {
	client *fakeCloudflareClient
}

func (f fakeCloudflareFactory) New(_, _ string) cfclient.Client { return f.client }

type fakeCloudflareClient struct {
	validateErr   error
	validateCalls int
	getTunnelErr  error
	getConfigErr  error
	getTokenErr   error
	deleteErr     error
	tunnel        *cfclient.Tunnel
	configuration []cfclient.IngressRule
	token         string
	createCalls   int
	updateCalls   int
	deleteCalls   int
}

func (f *fakeCloudflareClient) Validate(context.Context) error {
	f.validateCalls++
	return f.validateErr
}

func (f *fakeCloudflareClient) FindTunnelByName(_ context.Context, name string) (*cfclient.Tunnel, error) {
	if f.tunnel != nil && f.tunnel.Name == name {
		copy := *f.tunnel
		return &copy, nil
	}
	return nil, nil
}

func (f *fakeCloudflareClient) GetTunnel(_ context.Context, id string) (*cfclient.Tunnel, error) {
	if f.getTunnelErr != nil {
		return nil, f.getTunnelErr
	}
	if f.tunnel != nil && f.tunnel.ID == id {
		copy := *f.tunnel
		return &copy, nil
	}
	return nil, nil
}

func (f *fakeCloudflareClient) CreateTunnel(_ context.Context, name string) (*cfclient.Tunnel, error) {
	f.createCalls++
	f.tunnel = &cfclient.Tunnel{ID: testTunnelID, Name: name, ConfigSource: cfclient.ConfigSourceCloudflare}
	copy := *f.tunnel
	return &copy, nil
}

func (f *fakeCloudflareClient) GetConfiguration(context.Context, string) ([]cfclient.IngressRule, error) {
	if f.getConfigErr != nil {
		return nil, f.getConfigErr
	}
	return slices.Clone(f.configuration), nil
}

func (f *fakeCloudflareClient) UpdateConfiguration(_ context.Context, _ string, rules []cfclient.IngressRule) error {
	f.updateCalls++
	f.configuration = slices.Clone(rules)
	return nil
}

func (f *fakeCloudflareClient) GetToken(context.Context, string) (string, error) {
	if f.getTokenErr != nil {
		return "", f.getTokenErr
	}
	return f.token, nil
}

func (f *fakeCloudflareClient) DeleteTunnel(context.Context, string) error {
	f.deleteCalls++
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.tunnel = nil
	return nil
}

func currentCondition(conditionType string) metav1.Condition {
	return metav1.Condition{Type: conditionType, Status: metav1.ConditionTrue, Reason: "Test", ObservedGeneration: 1, LastTransitionTime: metav1.Now()}
}

func readyClusterProvider() *kflaredv1alpha1.ClusterCloudflareProvider {
	return &kflaredv1alpha1.ClusterCloudflareProvider{
		Name: testDefaultName, Generation: 1,
		Spec: kflaredv1alpha1.ClusterCloudflareProviderSpec{
			Controller:               testControllerClass,
			AccountID:                "0123456789abcdef0123456789abcdef",
			APITokenSecretRef:        kflaredv1alpha1.SecretKeyReference{Name: testAPITokenSecretName, Key: testAPITokenSecretKey},
			AllowedDNSZones:          []string{"example.com"},
			BindingNamespaceSelector: metav1.LabelSelector{MatchLabels: map[string]string{testTenantName: "allowed"}},
		},
		Status: kflaredv1alpha1.ClusterCloudflareProviderStatus{ObservedGeneration: 1, Conditions: []metav1.Condition{
			currentCondition(kflaredv1alpha1.ProviderConditionAccepted),
			currentCondition(kflaredv1alpha1.ProviderConditionCredentialsValid),
		}},
	}
}

func testDeployment(name, namespace string, labels map[string]string) *appsv1.Deployment {
	deployment := &appsv1.Deployment{}
	deployment.Name = name
	deployment.Namespace = namespace
	deployment.Labels = labels
	return deployment
}
