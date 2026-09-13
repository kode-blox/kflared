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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kflaredv1alpha1 "github.com/kode-blox/kflared/api/v1alpha1"
	cfclient "github.com/kode-blox/kflared/internal/cloudflare"
)

type fakeCloudflareFactory struct {
	client *fakeCloudflareClient
}

func (f fakeCloudflareFactory) New(_, _ string) cfclient.Client { return f.client }

type fakeCloudflareClient struct {
	validateErr   error
	tunnel        *cfclient.Tunnel
	configuration []cfclient.IngressRule
	token         string
	createCalls   int
	updateCalls   int
	deleteCalls   int
}

func (f *fakeCloudflareClient) Validate(context.Context) error { return f.validateErr }

func (f *fakeCloudflareClient) FindTunnelByName(_ context.Context, name string) (*cfclient.Tunnel, error) {
	if f.tunnel != nil && f.tunnel.Name == name {
		copy := *f.tunnel
		return &copy, nil
	}
	return nil, nil
}

func (f *fakeCloudflareClient) GetTunnel(_ context.Context, id string) (*cfclient.Tunnel, error) {
	if f.tunnel != nil && f.tunnel.ID == id {
		copy := *f.tunnel
		return &copy, nil
	}
	return nil, nil
}

func (f *fakeCloudflareClient) CreateTunnel(_ context.Context, name string) (*cfclient.Tunnel, error) {
	f.createCalls++
	f.tunnel = &cfclient.Tunnel{ID: "tunnel-id", Name: name, ConfigSource: cfclient.ConfigSourceCloudflare}
	copy := *f.tunnel
	return &copy, nil
}

func (f *fakeCloudflareClient) GetConfiguration(context.Context, string) ([]cfclient.IngressRule, error) {
	return slices.Clone(f.configuration), nil
}

func (f *fakeCloudflareClient) UpdateConfiguration(_ context.Context, _ string, rules []cfclient.IngressRule) error {
	f.updateCalls++
	f.configuration = slices.Clone(rules)
	return nil
}

func (f *fakeCloudflareClient) GetToken(context.Context, string) (string, error) { return f.token, nil }

func (f *fakeCloudflareClient) DeleteTunnel(context.Context, string) error {
	f.deleteCalls++
	f.tunnel = nil
	return nil
}

func currentCondition(conditionType string, status metav1.ConditionStatus) metav1.Condition {
	return metav1.Condition{Type: conditionType, Status: status, Reason: "Test", ObservedGeneration: 1, LastTransitionTime: metav1.Now()}
}

func readyProvider() *kflaredv1alpha1.CloudflareProvider {
	return &kflaredv1alpha1.CloudflareProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Generation: 1},
		Spec: kflaredv1alpha1.CloudflareProviderSpec{
			AccountID:                "0123456789abcdef0123456789abcdef",
			APITokenSecretRef:        kflaredv1alpha1.SecretKeyReference{Name: "cloudflare-api-token", Key: "api-token"},
			AllowedDNSZones:          []string{"example.com"},
			BindingNamespaceSelector: metav1.LabelSelector{MatchLabels: map[string]string{"tenant": "allowed"}},
		},
		Status: kflaredv1alpha1.CloudflareProviderStatus{ObservedGeneration: 1, Conditions: []metav1.Condition{
			currentCondition(kflaredv1alpha1.ProviderConditionAccepted, metav1.ConditionTrue),
			currentCondition(kflaredv1alpha1.ProviderConditionCredentialsValid, metav1.ConditionTrue),
		}},
	}
}
