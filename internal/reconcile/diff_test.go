// Copyright 2026 Paul Meyer
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package reconcile

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestChangedFields(t *testing.T) {
	type spec struct {
		Name    string          `json:"name"`
		Count   uint            `json:"count,omitempty"`
		Enabled bool            `json:"enabled,omitempty"`
		Tags    []string        `json:"tags,omitempty"`
		Limit   *uint           `json:"limit,omitempty"`
		Extra   json.RawMessage `json:"extra,omitempty"`
	}
	one := uint(1)

	tests := map[string]struct {
		want, have spec
		changed    []string
	}{
		"equal": {
			want: spec{Name: "a", Count: 1, Enabled: true, Tags: []string{"x"}, Limit: &one},
			have: spec{Name: "a", Count: 1, Enabled: true, Tags: []string{"x"}, Limit: &one},
		},
		"different values": {
			want:    spec{Name: "a", Count: 1, Tags: []string{"x"}},
			have:    spec{Name: "b", Count: 2, Tags: []string{"y"}},
			changed: []string{"count", "name", "tags"},
		},
		"zero value on one side": {
			want:    spec{Name: "a", Count: 1},
			have:    spec{Name: "a", Enabled: true, Limit: &one},
			changed: []string{"count", "enabled", "limit"},
		},
		"raw JSON is compared by content": {
			want: spec{Extra: json.RawMessage(`{"a":1,"b":[1,2]}`)},
			have: spec{Extra: json.RawMessage(`{ "b": [1, 2], "a": 1 }`)},
		},
		"raw JSON with different content": {
			want:    spec{Extra: json.RawMessage(`{"a":1}`)},
			have:    spec{Extra: json.RawMessage(`{"a":2}`)},
			changed: []string{"extra"},
		},
		"null is no value": {
			want: spec{Extra: json.RawMessage(`null`)},
			have: spec{},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			changed, err := changedFields(tt.want, tt.have)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(changed, tt.changed) {
				t.Errorf("changedFields() = %v, want %v", changed, tt.changed)
			}
		})
	}
}

func TestChangedFieldsRejectsInvalidJSON(t *testing.T) {
	type spec struct {
		Extra json.RawMessage `json:"extra"`
	}
	if _, err := changedFields(spec{Extra: json.RawMessage(`{`)}, spec{}); err == nil {
		t.Error("changedFields() accepted invalid JSON")
	}
}
