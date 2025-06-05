/*
Copyright 2025.

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

package controlplane

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestFormatStatusVersion tests the formatStatusVersion function
func TestFormatStatusVersion(t *testing.T) {
	tests := []struct {
		name           string
		specVersion    string
		statusVersion  string
		expectedStatus string
		description    string
	}{
		{
			name:           "spec without k0s suffix",
			specVersion:    "v1.33.1",
			statusVersion:  "v1.33.1-k0s.0",
			expectedStatus: "v1.33.1",
			description:    "When spec.version doesn't have -k0s suffix, status.version should also not have it",
		},
		{
			name:           "spec with k0s suffix",
			specVersion:    "v1.33.1-k0s.0",
			statusVersion:  "v1.33.1-k0s.0",
			expectedStatus: "v1.33.1-k0s.0",
			description:    "When spec.version has -k0s suffix, status.version should keep it",
		},
		{
			name:           "spec with custom k0s suffix",
			specVersion:    "v1.33.1-k0s.1",
			statusVersion:  "v1.33.1-k0s.1",
			expectedStatus: "v1.33.1-k0s.1",
			description:    "When spec.version has custom -k0s suffix, status.version should keep it",
		},
		{
			name:           "spec without suffix, status with different suffix",
			specVersion:    "v1.33.1",
			statusVersion:  "v1.33.1-k0s.1",
			expectedStatus: "v1.33.1",
			description:    "When spec.version doesn't have -k0s suffix, status.version should remove it even if it's different",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use the actual FormatStatusVersion function
			result := FormatStatusVersion(tt.specVersion, tt.statusVersion)
			assert.Equal(t, tt.expectedStatus, result, tt.description)
		})
	}
}
