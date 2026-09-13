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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelmCRDsMatchGeneratedManifests(t *testing.T) {
	tests := []struct {
		generated string
		chart     string
	}{
		{
			generated: filepath.Join("..", "..", "config", "crd", "bases", "kflared.kodeblox.com_cloudflareproviders.yaml"),
			chart:     filepath.Join("..", "..", "charts", "crds", "kflared.kodeblox.com_cloudflareproviders.yaml"),
		},
		{
			generated: filepath.Join("..", "..", "config", "crd", "bases", "kflared.kodeblox.com_cloudflaretunnelbindings.yaml"),
			chart:     filepath.Join("..", "..", "charts", "crds", "kflared.kodeblox.com_cloudflaretunnelbindings.yaml"),
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

func normalizeCRD(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "    \"helm.sh/resource-policy\": keep\n", "")
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(content), "---"))
}
