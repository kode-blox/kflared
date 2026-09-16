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
	"net/http"
	"net/http/httptest"
	"testing"

	cfapi "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/option"
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
				accountID: "test-account",
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
		accountID: "test-account",
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
