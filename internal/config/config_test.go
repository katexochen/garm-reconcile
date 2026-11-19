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
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudbase/garm/params"
)

const validYAML = `
admin:
  username: admin
  email: admin@example.com
  password: {_secret: /secrets/admin}
controller:
  metadata_url: http://10.0.0.1:9997/api/v1/metadata
  callback_url: http://10.0.0.1:9997/api/v1/callbacks
  agent_url: http://10.0.0.1:9997/agent
  webhook_url: http://10.0.0.1:9997/webhooks
github:
  credentials:
    main:
      endpoint: github.com
      pat:
        oauth2_token: {_secret: /secrets/pat}
  organizations:
    my-org:
      credentials_name: main
      pools:
        coco:
          provider_name: incus
          image: ubuntu
          flavor: default
          os_type: linux
          os_arch: amd64
          tags: [coco]
          max_runners: 2
          extra_specs:
            disable_updates: true
`

func staticSecrets(secrets map[string]string) SecretReader {
	return func(ref string) (string, error) {
		secret, ok := secrets[ref]
		if !ok {
			return "", os.ErrNotExist
		}
		return secret, nil
	}
}

func TestParse(t *testing.T) {
	secrets := staticSecrets(map[string]string{"/secrets/admin": "hunter2", "/secrets/pat": "ghp_token"})
	got, err := Parse([]byte(validYAML), secrets)
	if err != nil {
		t.Fatal(err)
	}

	want := &Config{
		Admin: Admin{Username: "admin", Email: "admin@example.com", Password: "hunter2"},
		Controller: Controller{
			MetadataURL: "http://10.0.0.1:9997/api/v1/metadata",
			CallbackURL: "http://10.0.0.1:9997/api/v1/callbacks",
			AgentURL:    "http://10.0.0.1:9997/agent",
			WebhookURL:  "http://10.0.0.1:9997/webhooks",
		},
		GitHub: Forge{
			Credentials: map[string]Credentials{
				"main": {Endpoint: "github.com", PAT: &PAT{OAuth2Token: "ghp_token"}},
			},
			Organizations: map[string]Entity{
				"my-org": {
					CredentialsName: "main",
					Pools: map[string]Pool{
						"coco": {CreatePoolParams: params.CreatePoolParams{
							ProviderName: "incus",
							Image:        "ubuntu",
							Flavor:       "default",
							OSType:       "linux",
							OSArch:       "amd64",
							Tags:         []string{"coco"},
							MaxRunners:   2,
							ExtraSpecs:   json.RawMessage(`{"disable_updates":true}`),
						}},
					},
				},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse() = %+v, want %+v", got, want)
	}
}

func TestParseJSON(t *testing.T) {
	const doc = `{
		"admin": {"username": "admin", "email": "admin@example.com", "password": "hunter2"},
		"controller": {
			"metadata_url": "http://garm/api/v1/metadata",
			"callback_url": "http://garm/api/v1/callbacks",
			"agent_url": "http://garm/agent",
			"webhook_url": "http://garm/webhooks"
		},
		"github": {"credentials": {"app": {"endpoint": "github.com", "app": {
			"app_id": 9007199254740993, "installation_id": 2, "private_key": "pem"
		}}}}
	}`
	cfg, err := Parse([]byte(doc), PlaceholderSecrets)
	if err != nil {
		t.Fatal(err)
	}
	// 2^53+1 is not representable as a float64.
	if got := cfg.GitHub.Credentials["app"].App.AppID; got != 9007199254740993 {
		t.Errorf("app_id = %d, want 9007199254740993", got)
	}
}

func TestParseErrors(t *testing.T) {
	tests := map[string]struct {
		doc     string
		wantErr string
	}{
		"unknown field": {
			doc:     strings.Replace(validYAML, "username: admin", "username: admin\n  user_name: admin", 1),
			wantErr: `unknown field "user_name"`,
		},
		"duplicate key": {
			doc:     validYAML + "admin: {}\n",
			wantErr: `key "admin" already set`,
		},
		"unreadable secret": {
			doc:     strings.Replace(validYAML, "/secrets/pat", "/secrets/missing", 1),
			wantErr: "github.credentials.main.pat.oauth2_token: reading secret",
		},
		"secret reference with extra keys": {
			doc:     strings.Replace(validYAML, "{_secret: /secrets/admin}", "{_secret: /secrets/admin, encoding: base64}", 1),
			wantErr: "admin.password: a secret reference must have the form",
		},
		"secret reference without path": {
			doc:     strings.Replace(validYAML, "{_secret: /secrets/admin}", "{_secret: 1}", 1),
			wantErr: "admin.password: a secret reference must have the form",
		},
		"invalid desired state": {
			doc:     strings.Replace(validYAML, "credentials_name: main", "credentials_name: other", 1),
			wantErr: `credentials "other" are not declared`,
		},
	}
	secrets := staticSecrets(map[string]string{"/secrets/admin": "hunter2", "/secrets/pat": "ghp_token"})
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tt.doc), secrets)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Parse() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseSecretInList(t *testing.T) {
	doc := strings.Replace(validYAML, "tags: [coco]", "tags: [{_secret: /secrets/tag}]", 1)
	secrets := staticSecrets(map[string]string{
		"/secrets/admin": "hunter2", "/secrets/pat": "ghp_token", "/secrets/tag": "from-file",
	})
	cfg, err := Parse([]byte(doc), secrets)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.GitHub.Organizations["my-org"].Pools["coco"].Tags
	if want := []string{"from-file"}; !reflect.DeepEqual(got, want) {
		t.Errorf("tags = %v, want %v", got, want)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "garm.yaml")
	if err := os.WriteFile(path, []byte(validYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, PlaceholderSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Admin.Password; got != "<secret>" {
		t.Errorf("password = %q, want the placeholder", got)
	}

	_, err = Load(filepath.Join(dir, "missing.yaml"), PlaceholderSecrets)
	if !os.IsNotExist(err) {
		t.Errorf("Load() of a missing file: error = %v, want not exist", err)
	}

	if err := os.WriteFile(path, []byte("admin: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Load(path, PlaceholderSecrets)
	if err == nil || !strings.HasPrefix(err.Error(), path+": ") {
		t.Errorf("Load() of a malformed file: error = %v, want it to name the file", err)
	}
}

func TestFileSecrets(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	token := write("token", "ghp_token\n")
	key := write("key", "-----BEGIN KEY-----\nabc\n-----END KEY-----\r\n")
	empty := write("empty", "\n")

	tests := map[string]struct {
		dir     string
		ref     string
		want    string
		wantErr string
	}{
		"absolute path":                {ref: token, want: "ghp_token"},
		"only trailing newlines":       {ref: key, want: "-----BEGIN KEY-----\nabc\n-----END KEY-----"},
		"relative to directory":        {dir: dir, ref: "token", want: "ghp_token"},
		"relative without a directory": {ref: "token", wantErr: "$CREDENTIALS_DIRECTORY"},
		"empty file":                   {ref: empty, wantErr: "is empty"},
		"missing file":                 {dir: dir, ref: "missing", wantErr: "no such file"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := FileSecrets(tt.dir)(tt.ref)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("secret = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestReadmeExample checks that the desired state the README shows is valid.
func TestReadmeExample(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, example, found := strings.Cut(string(readme), "```yaml\n")
	example, _, closed := strings.Cut(example, "```")
	if !found || !closed {
		t.Fatal("the README has no YAML example")
	}
	if _, err := Parse([]byte(example), PlaceholderSecrets); err != nil {
		t.Errorf("the example of the README is rejected: %v", err)
	}
}
