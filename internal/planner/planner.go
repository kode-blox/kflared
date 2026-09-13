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

// Package planner contains deterministic, side-effect-free reconciliation planning.
package planner

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"

	kflaredv1alpha1 "github.com/kode-blox/kflared/api/v1alpha1"
	cfclient "github.com/kode-blox/kflared/internal/cloudflare"
)

const maxTunnelNameLength = 100

// NormalizeZones validates and canonicalizes an allow-list.
func NormalizeZones(zones []string) ([]string, error) {
	unique := make(map[string]struct{}, len(zones))
	for _, zone := range zones {
		zone = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(zone), "."))
		if zone == "" || strings.HasPrefix(zone, "*.") || len(validation.IsDNS1123Subdomain(zone)) != 0 {
			return nil, fmt.Errorf("%q is not a concrete DNS zone", zone)
		}
		unique[zone] = struct{}{}
	}
	result := make([]string, 0, len(unique))
	for zone := range unique {
		result = append(result, zone)
	}
	sort.Strings(result)
	return result, nil
}

// NormalizeHostnames returns sorted, unique, concrete hostnames within allowed zones.
func NormalizeHostnames(hostnames, zones []string) ([]string, []string) {
	allowed := make(map[string]struct{}, len(hostnames))
	rejected := make(map[string]struct{})
	for _, hostname := range hostnames {
		hostname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostname), "."))
		valid := hostname != "" && !strings.HasPrefix(hostname, "*.") && len(validation.IsDNS1123Subdomain(hostname)) == 0
		if valid {
			valid = false
			for _, zone := range zones {
				if hostname == zone || strings.HasSuffix(hostname, "."+zone) {
					valid = true
					break
				}
			}
		}
		if valid {
			allowed[hostname] = struct{}{}
		} else if hostname != "" {
			rejected[hostname] = struct{}{}
		}
	}
	return sortedKeys(allowed), sortedKeys(rejected)
}

// IngressRules maps each public hostname to the same internal Gateway Service.
func IngressRules(hostnames []string, originService string) []cfclient.IngressRule {
	rules := make([]cfclient.IngressRule, 0, len(hostnames)+1)
	for _, hostname := range hostnames {
		rules = append(rules, cfclient.IngressRule{Hostname: hostname, Service: originService})
	}
	rules = append(rules, cfclient.IngressRule{Service: "http_status:404"})
	return rules
}

// EqualIngress compares canonical Cloudflare configurations.
func EqualIngress(left, right []cfclient.IngressRule) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// TunnelName embeds the complete binding UID so a retry can only recover the
// tunnel created for that exact Kubernetes object. The readable parts are bounded.
func TunnelName(clusterUID types.UID, binding *kflaredv1alpha1.CloudflareTunnelBinding) string {
	cluster := compactID(string(clusterUID), 8)
	namespace := sanitize(binding.Namespace, 14)
	gateway := sanitize(binding.Spec.GatewayRef.Name, 14)
	uid := sanitize(string(binding.UID), 36)
	name := fmt.Sprintf("kflared-%s-%s-%s-%s", cluster, namespace, gateway, uid)
	if len(name) > maxTunnelNameLength {
		return name[:maxTunnelNameLength]
	}
	return name
}

// BindingPrecedes defines stable ownership for Gateway and hostname conflicts.
func BindingPrecedes(left, right *kflaredv1alpha1.CloudflareTunnelBinding) bool {
	if !left.CreationTimestamp.Equal(&right.CreationTimestamp) {
		return left.CreationTimestamp.Before(&right.CreationTimestamp)
	}
	leftKey := left.Namespace + "/" + left.Name + "/" + string(left.UID)
	rightKey := right.Namespace + "/" + right.Name + "/" + string(right.UID)
	return leftKey < rightKey
}

func sortedKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func compactID(value string, limit int) string {
	value = strings.ReplaceAll(value, "-", "")
	if len(value) > limit {
		return value[:limit]
	}
	if value == "" {
		return "unknown"
	}
	return value
}

func sanitize(value string, limit int) string {
	value = strings.ToLower(value)
	var result strings.Builder
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '-' {
			result.WriteRune(character)
		} else {
			result.WriteByte('-')
		}
	}
	clean := strings.Trim(result.String(), "-")
	if clean == "" {
		clean = "unknown"
	}
	if len(clean) > limit {
		clean = strings.TrimRight(clean[:limit], "-")
	}
	return clean
}
