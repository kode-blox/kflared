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

package main

import "testing"

func TestValidateControllerClass(t *testing.T) {
	for _, value := range []string{"", " ", "\t\n"} {
		if err := validateControllerClass(value); err == nil {
			t.Errorf("validateControllerClass(%q) succeeded, want error", value)
		}
	}
	if err := validateControllerClass("production"); err != nil {
		t.Fatalf("validateControllerClass(non-empty) = %v", err)
	}
}

func TestLeaderElectionIDIsStableAndClassSpecific(t *testing.T) {
	first := leaderElectionID("production")
	if first == "" || len(first) > 253 {
		t.Fatalf("leader election ID %q is empty or too long", first)
	}
	if got := leaderElectionID("production"); got != first {
		t.Fatalf("leader election ID changed: %q then %q", first, got)
	}
	if got := leaderElectionID("staging"); got == first {
		t.Fatalf("different controller classes share leader election ID %q", first)
	}
}
