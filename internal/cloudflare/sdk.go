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

package cloudflare

import (
	"context"
	"errors"
	"fmt"

	cfapi "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/option"
	"github.com/cloudflare/cloudflare-go/v7/zero_trust"
)

// SDKFactory builds clients backed by Cloudflare's official Go SDK.
type SDKFactory struct{}

func (SDKFactory) New(apiToken, accountID string) Client {
	return &sdkClient{
		api:       cfapi.NewClient(option.WithAPIToken(apiToken)),
		accountID: accountID,
	}
}

type sdkClient struct {
	api       *cfapi.Client
	accountID string
}

func (c *sdkClient) Validate(ctx context.Context) error {
	_, err := c.api.ZeroTrust.Tunnels.Cloudflared.List(ctx, zero_trust.TunnelCloudflaredListParams{
		AccountID: cfapi.F(c.accountID),
		PerPage:   cfapi.F(1.0),
		IsDeleted: cfapi.F(false),
	})
	if isCredentialRejection(err) {
		return errors.Join(ErrCredentialsRejected, err)
	}
	return err
}

func (c *sdkClient) FindTunnelByName(ctx context.Context, name string) (*Tunnel, error) {
	page, err := c.api.ZeroTrust.Tunnels.Cloudflared.List(ctx, zero_trust.TunnelCloudflaredListParams{
		AccountID: cfapi.F(c.accountID),
		Name:      cfapi.F(name),
		PerPage:   cfapi.F(100.0),
		IsDeleted: cfapi.F(false),
	})
	if err != nil {
		return nil, err
	}
	for _, candidate := range page.Result {
		if candidate.Name == name {
			return &Tunnel{ID: candidate.ID, Name: candidate.Name, ConfigSource: string(candidate.ConfigSrc), Status: string(candidate.Status)}, nil
		}
	}
	return nil, nil
}

func (c *sdkClient) GetTunnel(ctx context.Context, id string) (*Tunnel, error) {
	result, err := c.api.ZeroTrust.Tunnels.Cloudflared.Get(ctx, id, zero_trust.TunnelCloudflaredGetParams{
		AccountID: cfapi.F(c.accountID),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &Tunnel{ID: result.ID, Name: result.Name, ConfigSource: string(result.ConfigSrc), Status: string(result.Status)}, nil
}

func (c *sdkClient) CreateTunnel(ctx context.Context, name string) (*Tunnel, error) {
	result, err := c.api.ZeroTrust.Tunnels.Cloudflared.New(ctx, zero_trust.TunnelCloudflaredNewParams{
		AccountID: cfapi.F(c.accountID),
		Name:      cfapi.F(name),
		ConfigSrc: cfapi.F(zero_trust.TunnelCloudflaredNewParamsConfigSrcCloudflare),
	})
	if err != nil {
		return nil, err
	}
	return &Tunnel{ID: result.ID, Name: result.Name, ConfigSource: string(result.ConfigSrc), Status: string(result.Status)}, nil
}

func (c *sdkClient) GetConfiguration(ctx context.Context, id string) ([]IngressRule, error) {
	result, err := c.api.ZeroTrust.Tunnels.Cloudflared.Configurations.Get(ctx, id, zero_trust.TunnelCloudflaredConfigurationGetParams{
		AccountID: cfapi.F(c.accountID),
	})
	if err != nil {
		return nil, err
	}
	rules := make([]IngressRule, 0, len(result.Config.Ingress))
	for _, rule := range result.Config.Ingress {
		rules = append(rules, IngressRule{Hostname: rule.Hostname, Service: rule.Service, Path: rule.Path})
	}
	return rules, nil
}

func (c *sdkClient) UpdateConfiguration(ctx context.Context, id string, rules []IngressRule) error {
	ingress := make([]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, 0, len(rules))
	for _, rule := range rules {
		item := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{Service: cfapi.F(rule.Service)}
		if rule.Hostname != "" {
			item.Hostname = cfapi.F(rule.Hostname)
		}
		if rule.Path != "" {
			item.Path = cfapi.F(rule.Path)
		}
		ingress = append(ingress, item)
	}
	_, err := c.api.ZeroTrust.Tunnels.Cloudflared.Configurations.Update(ctx, id, zero_trust.TunnelCloudflaredConfigurationUpdateParams{
		AccountID: cfapi.F(c.accountID),
		Config: cfapi.F(zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfig{
			Ingress: cfapi.F(ingress),
		}),
	})
	return err
}

func (c *sdkClient) GetToken(ctx context.Context, id string) (string, error) {
	result, err := c.api.ZeroTrust.Tunnels.Cloudflared.Token.Get(ctx, id, zero_trust.TunnelCloudflaredTokenGetParams{
		AccountID: cfapi.F(c.accountID),
	})
	if err != nil {
		return "", err
	}
	if result == nil || *result == "" {
		return "", fmt.Errorf("cloudflare returned an empty tunnel token")
	}
	return *result, nil
}

func (c *sdkClient) DeleteTunnel(ctx context.Context, id string) error {
	_, err := c.api.ZeroTrust.Tunnels.Cloudflared.Delete(ctx, id, zero_trust.TunnelCloudflaredDeleteParams{
		AccountID: cfapi.F(c.accountID),
	})
	if isNotFound(err) {
		return nil
	}
	return err
}

func (c *sdkClient) ListPrivateRoutes(ctx context.Context, network string) ([]PrivateRoute, error) {
	// The API's network_subset filter can include narrower routes. Compare the
	// returned CIDR exactly so a /32 never adopts or deletes a neighboring route.
	pager := c.api.ZeroTrust.Networks.Routes.ListAutoPaging(ctx, zero_trust.NetworkRouteListParams{
		AccountID:     cfapi.F(c.accountID),
		NetworkSubset: cfapi.F(network),
		IsDeleted:     cfapi.F(false),
	})
	var routes []PrivateRoute
	for pager.Next() {
		candidate := pager.Current()
		if candidate.Network == network && candidate.DeletedAt.IsZero() {
			routes = append(routes, PrivateRoute{ID: candidate.ID, Network: candidate.Network, TunnelID: candidate.TunnelID, Comment: candidate.Comment, VirtualNetworkID: candidate.VirtualNetworkID})
		}
	}
	return routes, pager.Err()
}

func (c *sdkClient) GetPrivateRoute(ctx context.Context, id string) (*PrivateRoute, error) {
	result, err := c.api.ZeroTrust.Networks.Routes.Get(ctx, id, zero_trust.NetworkRouteGetParams{AccountID: cfapi.F(c.accountID)})
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !result.DeletedAt.IsZero() {
		return nil, nil
	}
	return &PrivateRoute{ID: result.ID, Network: result.Network, TunnelID: result.TunnelID, Comment: result.Comment, VirtualNetworkID: result.VirtualNetworkID}, nil
}

func (c *sdkClient) CreatePrivateRoute(ctx context.Context, network, tunnelID, comment string) (*PrivateRoute, error) {
	params := zero_trust.NetworkRouteNewParams{
		AccountID: cfapi.F(c.accountID),
		Network:   cfapi.F(network),
		TunnelID:  cfapi.F(tunnelID),
	}
	if comment != "" {
		params.Comment = cfapi.F(comment)
	}
	result, err := c.api.ZeroTrust.Networks.Routes.New(ctx, params)
	if err != nil {
		return nil, err
	}
	return &PrivateRoute{ID: result.ID, Network: result.Network, TunnelID: result.TunnelID, Comment: result.Comment, VirtualNetworkID: result.VirtualNetworkID}, nil
}

func (c *sdkClient) DeletePrivateRoute(ctx context.Context, id string) error {
	_, err := c.api.ZeroTrust.Networks.Routes.Delete(ctx, id, zero_trust.NetworkRouteDeleteParams{AccountID: cfapi.F(c.accountID)})
	if isNotFound(err) {
		return nil
	}
	return err
}

func isNotFound(err error) bool {
	var apiErr *cfapi.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == 404
}

func isCredentialRejection(err error) bool {
	var apiErr *cfapi.Error
	return errors.As(err, &apiErr) && (apiErr.StatusCode == 401 || apiErr.StatusCode == 403)
}
