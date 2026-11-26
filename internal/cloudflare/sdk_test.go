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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cfapi "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/option"
)

const (
	testCloudflareAccountID = "test-account"
	testPrivateRouteCIDR    = "10.43.0.1/32"
)

func TestSDKClientValidateClassifiesOnlyCredentialRejections(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		rejected   bool
	}{
		{name: "unauthorized", statusCode: http.StatusUnauthorized, rejected: true},
		{name: "forbidden", statusCode: http.StatusForbidden, rejected: true},
		{name: "rate limited", statusCode: http.StatusTooManyRequests},
		{name: "server error", statusCode: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"test failure"}],"messages":[],"result":null}`))
			}))
			defer server.Close()

			client := &sdkClient{
				api: cfapi.NewClient(
					option.WithAPIToken("test-token"),
					option.WithBaseURL(server.URL+"/"),
					option.WithMaxRetries(0),
				),
				accountID: testCloudflareAccountID,
			}
			err := client.Validate(context.Background())
			if err == nil {
				t.Fatal("Validate() error = nil, want an SDK error")
			}
			if got := IsCredentialRejected(err); got != tt.rejected {
				t.Errorf("IsCredentialRejected() = %t, want %t", got, tt.rejected)
			}
			var apiErr *cfapi.Error
			if !errors.As(err, &apiErr) {
				t.Fatalf("Validate() error %T does not preserve *cloudflare.Error", err)
			}
			if apiErr.StatusCode != tt.statusCode {
				t.Errorf("preserved status code = %d, want %d", apiErr.StatusCode, tt.statusCode)
			}
		})
	}
}

func TestSDKClientValidateLeavesTransportErrorsRetryable(t *testing.T) {
	transportErr := errors.New("transport unavailable")
	client := &sdkClient{
		api: cfapi.NewClient(
			option.WithAPIToken("test-token"),
			option.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, transportErr
			})}),
			option.WithMaxRetries(0),
		),
		accountID: testCloudflareAccountID,
	}

	err := client.Validate(context.Background())
	if !errors.Is(err, transportErr) {
		t.Fatalf("Validate() error = %v, want preserved transport error", err)
	}
	if IsCredentialRejected(err) {
		t.Fatal("transport error was classified as a credential rejection")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestSDKClientGetTunnelDistinguishesDeletedFromExisting(t *testing.T) {
	const tunnelID = "test-tunnel"
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       *Tunnel
		wantError  bool
	}{
		{
			name:       "deleted object",
			statusCode: http.StatusOK,
			body:       `{"success":true,"result":{"id":"test-tunnel","deleted_at":"2026-11-26T12:00:00Z","status":"inactive"}}`,
		},
		{
			name:       "existing local tunnel",
			statusCode: http.StatusOK,
			body:       `{"success":true,"result":{"id":"test-tunnel","name":"other","config_src":"local","deleted_at":null,"status":"down"}}`,
			want:       &Tunnel{ID: tunnelID, Name: "other", ConfigSource: "local", Status: "down"},
		},
		{
			name:       "existing tunnel without deletion field",
			statusCode: http.StatusOK,
			body:       `{"success":true,"result":{"id":"test-tunnel","name":"remote-tunnel","config_src":"cloudflare","status":"inactive"}}`,
			want:       &Tunnel{ID: tunnelID, Name: "remote-tunnel", ConfigSource: ConfigSourceCloudflare, Status: "inactive"},
		},
		{
			name:       "not found",
			statusCode: http.StatusNotFound,
			body:       `{"success":false,"errors":[{"code":1000,"message":"test failure"}]}`,
		},
		{
			name:       "forbidden",
			statusCode: http.StatusForbidden,
			body:       `{"success":false,"errors":[{"code":1000,"message":"test failure"}]}`,
			wantError:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &sdkClient{
				api: cfapi.NewClient(
					option.WithAPIToken("test-token"),
					option.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						if req.Method != http.MethodGet || req.URL.Path != "/client/v4/accounts/"+testCloudflareAccountID+"/cfd_tunnel/"+tunnelID {
							t.Errorf("unexpected tunnel request %s %s", req.Method, req.URL.Path)
						}
						return &http.Response{
							StatusCode: tt.statusCode,
							Header:     http.Header{"Content-Type": {"application/json"}},
							Body:       io.NopCloser(strings.NewReader(tt.body)),
						}, nil
					})}),
					option.WithMaxRetries(0),
				),
				accountID: testCloudflareAccountID,
			}
			tunnel, err := client.GetTunnel(t.Context(), tunnelID)
			if tt.wantError {
				var apiErr *cfapi.Error
				if !errors.As(err, &apiErr) || apiErr.StatusCode != tt.statusCode {
					t.Fatalf("GetTunnel() error = %v, want status %d", err, tt.statusCode)
				}
			} else if err != nil {
				t.Fatalf("GetTunnel() error = %v", err)
			}
			if (tt.want == nil && tunnel != nil) || (tt.want != nil && (tunnel == nil || *tunnel != *tt.want)) {
				t.Errorf("GetTunnel() = %#v, want %#v", tunnel, tt.want)
			}
		})
	}
}

func TestSDKClientPrivateRoutes(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/accounts/"+testCloudflareAccountID+"/teamnet/routes") {
			t.Errorf("unexpected route path %q", r.URL.Path)
		}
		methods = append(methods, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			if r.URL.Path == "/accounts/"+testCloudflareAccountID+"/teamnet/routes" {
				if r.URL.Query().Get("page") == "2" {
					_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":[],"result_info":{"page":2,"per_page":20,"count":0,"total_count":2}}`))
					return
				}
				if r.URL.Query().Get("network_subset") != testPrivateRouteCIDR || r.URL.Query().Get("is_deleted") != "false" {
					t.Errorf("unexpected list query %q", r.URL.RawQuery)
				}
				_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":[{"id":"owned","network":"` + testPrivateRouteCIDR + `","tunnel_id":"tunnel","comment":"owner"},{"id":"other","network":"10.43.0.2/32","tunnel_id":"other"}],"result_info":{"page":1,"per_page":20,"count":2,"total_count":2}}`))
			} else {
				_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"id":"owned","network":"` + testPrivateRouteCIDR + `","tunnel_id":"tunnel","comment":"owner"}}`))
			}
		case http.MethodPost:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["network"] != testPrivateRouteCIDR || body["tunnel_id"] != "tunnel" || body["comment"] != "owner" {
				t.Errorf("unexpected create body: %#v", body)
			}
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"id":"owned","network":"` + testPrivateRouteCIDR + `","tunnel_id":"tunnel","comment":"owner"}}`))
		case http.MethodDelete:
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"id":"owned"}}`))
		}
	}))
	defer server.Close()
	client := &sdkClient{api: cfapi.NewClient(option.WithAPIToken("test-token"), option.WithBaseURL(server.URL+"/"), option.WithMaxRetries(0)), accountID: testCloudflareAccountID}
	ctx := context.Background()
	routes, err := client.ListPrivateRoutes(ctx, testPrivateRouteCIDR)
	if err != nil || len(routes) != 1 || routes[0].ID != "owned" {
		t.Fatalf("ListPrivateRoutes() = %#v, %v", routes, err)
	}
	route, err := client.GetPrivateRoute(ctx, "owned")
	if err != nil || route == nil || route.TunnelID != "tunnel" {
		t.Fatalf("GetPrivateRoute() = %#v, %v", route, err)
	}
	route, err = client.CreatePrivateRoute(ctx, testPrivateRouteCIDR, "tunnel", "owner")
	if err != nil || route == nil || route.ID != "owned" {
		t.Fatalf("CreatePrivateRoute() = %#v, %v", route, err)
	}
	if err := client.DeletePrivateRoute(ctx, "owned"); err != nil {
		t.Fatal(err)
	}
	if got := methods[len(methods)-1]; got != "DELETE /accounts/"+testCloudflareAccountID+"/teamnet/routes/owned" {
		t.Errorf("delete endpoint = %q", got)
	}
}
