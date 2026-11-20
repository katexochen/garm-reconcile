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

package config

import (
	"bytes"
	"encoding/json"

	"sigs.k8s.io/yaml"
)

// AdminPasswordSecret is the secret Marshal references for the password of
// the admin user.
const AdminPasswordSecret = "garm-admin-password"

// Marshal returns cfg as a YAML state file, without settings that are
// empty. The password of the admin user and the secrets of credentials are
// not written: they become references to secrets named after what they
// belong to, like github-cred1-token. Webhook secrets are written as they
// are.
func Marshal(cfg *Config) ([]byte, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	// See Parse on the numbers.
	var tree map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}

	tree["admin"].(map[string]any)["password"] = secretRef(AdminPasswordSecret)
	for _, forge := range []string{"github", "gitea"} {
		forgeTree := tree[forge].(map[string]any)
		if len(forgeTree) == 0 {
			delete(tree, forge)
			continue
		}
		credentials, _ := forgeTree["credentials"].(map[string]any)
		for name, creds := range credentials {
			creds := creds.(map[string]any)
			if pat, ok := creds["pat"].(map[string]any); ok {
				pat["oauth2_token"] = secretRef(forge + "-" + name + "-token")
			}
			if app, ok := creds["app"].(map[string]any); ok {
				app["private_key"] = secretRef(forge + "-" + name + "-private-key")
			}
		}
	}

	data, err = json.Marshal(withoutEmpty(tree))
	if err != nil {
		return nil, err
	}
	return yaml.JSONToYAML(data)
}

// withoutEmpty removes the settings of a decoded document that have no
// value. Among them are those the requests of garm always carry and a state
// file must not set, like the ID of a scale set.
func withoutEmpty(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	for key, elem := range m {
		switch elem := withoutEmpty(elem).(type) {
		case nil:
			delete(m, key)
		case string:
			if elem == "" {
				delete(m, key)
			}
		case json.Number:
			if key == "scale_set_id" && elem == "0" {
				delete(m, key)
			}
		}
	}
	return m
}

func secretRef(name string) map[string]any {
	return map[string]any{secretKey: name}
}
