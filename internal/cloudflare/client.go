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

// Package cloudflare isolates the controller from the external API and keeps
// credentials out of Kubernetes reconciliation data structures.
package cloudflare

import (
	"context"
	"errors"
)

const ConfigSourceCloudflare = "cloudflare"

// ErrCredentialsRejected marks a validation failure that conclusively proves
// Cloudflare rejected the configured credentials.
var ErrCredentialsRejected = errors.New("cloudflare credentials rejected")

// IsCredentialRejected reports whether validation conclusively proved that
// Cloudflare rejected the configured credentials.
func IsCredentialRejected(err error) bool {
	return errors.Is(err, ErrCredentialsRejected)
}

// Tunnel is the external state needed by the reconciler.
type Tunnel struct {
	ID           string
	Name         string
	ConfigSource string
	Status       string
}

// IngressRule is one remotely managed cloudflared ingress rule.
type IngressRule struct {
	Hostname string
	Service  string
	Path     string
}

// PrivateRoute is an account-level CIDR route. Its ID is the handle used for
// ownership checks and deletion; network and tunnel ID alone are not ownership.
type PrivateRoute struct {
	ID               string
	Network          string
	TunnelID         string
	Comment          string
	VirtualNetworkID string
}

// Client describes the least Cloudflare API surface required by the MVP.
type Client interface {
	Validate(ctx context.Context) error
	FindTunnelByName(ctx context.Context, name string) (*Tunnel, error)
	GetTunnel(ctx context.Context, id string) (*Tunnel, error)
	CreateTunnel(ctx context.Context, name string) (*Tunnel, error)
	GetConfiguration(ctx context.Context, id string) ([]IngressRule, error)
	UpdateConfiguration(ctx context.Context, id string, rules []IngressRule) error
	GetToken(ctx context.Context, id string) (string, error)
	DeleteTunnel(ctx context.Context, id string) error
	ListPrivateRoutes(ctx context.Context, network string) ([]PrivateRoute, error)
	GetPrivateRoute(ctx context.Context, id string) (*PrivateRoute, error)
	CreatePrivateRoute(ctx context.Context, network, tunnelID, comment string) (*PrivateRoute, error)
	DeletePrivateRoute(ctx context.Context, id string) error
}

// Factory creates an account-scoped API client from a token read from a Secret.
type Factory interface {
	New(apiToken, accountID string) Client
}
