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
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

func TestHelmCRDsMatchGeneratedManifests(t *testing.T) {
	tests := []struct {
		generated string
		chart     string
	}{
		{
			generated: filepath.Join("..", "..", "config", "crd", "bases", "kflared.kodeblox.com_clustercloudflareproviders.yaml"),
			chart:     filepath.Join("..", "..", "charts", "crds", "kflared.kodeblox.com_clustercloudflareproviders.yaml"),
		},
		{
			generated: filepath.Join("..", "..", "config", "crd", "bases", "kflared.kodeblox.com_cloudflaretunnelbindings.yaml"),
			chart:     filepath.Join("..", "..", "charts", "crds", "kflared.kodeblox.com_cloudflaretunnelbindings.yaml"),
		},
		{
			generated: filepath.Join("..", "..", "config", "crd", "bases", "kflared.kodeblox.com_cloudflaretunnelprivateroutes.yaml"),
			chart:     filepath.Join("..", "..", "charts", "crds", "kflared.kodeblox.com_cloudflaretunnelprivateroutes.yaml"),
		},
	}
	for _, test := range tests {
		generated, err := os.ReadFile(test.generated)
		if err != nil {
			t.Fatal(err)
		}
		chart, err := os.ReadFile(test.chart)
		if err != nil {
			t.Fatal(err)
		}
		want := normalizeCRD(string(generated))
		if got := normalizeCRD(string(chart)); got != want {
			t.Errorf("%s does not match %s; regenerate manifests and synchronize the chart CRD", test.chart, test.generated)
		}
	}
}

func TestHelmManagerRBACMatchesGeneratedManifest(t *testing.T) {
	generatedPath := filepath.Join("..", "..", "config", "rbac", "role.yaml")
	chartPath := filepath.Join("..", "..", "charts", "templates", "clusterrole.yaml")

	generatedRules := readClusterRoleRules(t, generatedPath, false)
	chartRules := readClusterRoleRules(t, chartPath, true)
	if !reflect.DeepEqual(chartRules, generatedRules) {
		got, _ := json.MarshalIndent(chartRules, "", "  ")
		want, _ := json.MarshalIndent(generatedRules, "", "  ")
		t.Fatalf("%s manager rules do not match %s\ngot:\n%s\nwant:\n%s", chartPath, generatedPath, got, want)
	}
}

func TestManagerRBACAllowsMainResourceUpdatesForFinalizers(t *testing.T) {
	generatedPath := filepath.Join("..", "..", "config", "rbac", "role.yaml")
	rules := readClusterRoleRules(t, generatedPath, false)

	for _, resource := range []string{"clustercloudflareproviders", "cloudflaretunnelbindings", "cloudflaretunnelprivateroutes"} {
		if !allowsResourceVerb(rules, "kflared.kodeblox.com", resource, "update") {
			t.Errorf("%s must allow update on %s so the controller can manage finalizers", generatedPath, resource)
		}
	}
}

func allowsResourceVerb(rules []rbacv1.PolicyRule, apiGroup, resource, verb string) bool {
	for _, rule := range rules {
		if slices.Contains(rule.APIGroups, apiGroup) && slices.Contains(rule.Resources, resource) && slices.Contains(rule.Verbs, verb) {
			return true
		}
	}
	return false
}

func readClusterRoleRules(t *testing.T, path string, helmTemplate bool) []rbacv1.PolicyRule {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if helmTemplate {
		content = []byte(staticManagerClusterRole(string(content)))
	}

	jsonContent, err := utilyaml.ToJSON(content)
	if err != nil {
		t.Fatalf("convert %s to JSON: %v", path, err)
	}
	role := &rbacv1.ClusterRole{}
	if err := json.Unmarshal(jsonContent, role); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return normalizePolicyRules(role.Rules)
}

func staticManagerClusterRole(content string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "{{- if and .Values.metrics.enabled") {
			break
		}
		if strings.Contains(line, `name: {{ include "kflared.fullname" . }}-manager`) {
			result = append(result, "  name: manager-role")
			continue
		}
		if strings.Contains(line, "{{") {
			continue
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

func normalizePolicyRules(rules []rbacv1.PolicyRule) []rbacv1.PolicyRule {
	normalized := append([]rbacv1.PolicyRule(nil), rules...)
	for i := range normalized {
		slices.Sort(normalized[i].APIGroups)
		slices.Sort(normalized[i].Resources)
		slices.Sort(normalized[i].ResourceNames)
		slices.Sort(normalized[i].NonResourceURLs)
		slices.Sort(normalized[i].Verbs)
	}
	slices.SortFunc(normalized, func(leftRule, rightRule rbacv1.PolicyRule) int {
		left, _ := json.Marshal(leftRule)
		right, _ := json.Marshal(rightRule)
		return strings.Compare(string(left), string(right))
	})
	return normalized
}

func normalizeCRD(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "    \"helm.sh/resource-policy\": keep\n", "")
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(content), "---"))
}
