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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// secretKey marks an object as a reference to a secret: {"_secret": "<path>"}.
const secretKey = "_secret"

// SecretReader returns the secret a reference in the desired state points to.
type SecretReader func(ref string) (string, error)

// FileSecrets reads secrets from files. Relative references are resolved
// against dir, which is usually systemd's $CREDENTIALS_DIRECTORY.
func FileSecrets(dir string) SecretReader {
	return func(ref string) (string, error) {
		path := ref
		if !filepath.IsAbs(path) {
			if dir == "" {
				return "", errors.New("relative secret paths require $CREDENTIALS_DIRECTORY to be set")
			}
			path = filepath.Join(dir, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		secret := strings.TrimRight(string(data), "\r\n")
		if secret == "" {
			return "", fmt.Errorf("%s is empty", path)
		}
		return secret, nil
	}
}

// PlaceholderSecrets substitutes a placeholder for every secret, which
// allows validating the desired state where the secrets are not available.
func PlaceholderSecrets(string) (string, error) {
	return "<secret>", nil
}

// resolveSecrets replaces all secret references in the decoded document v.
// path locates v in the document and is used in error messages.
func resolveSecrets(v any, path string, read SecretReader) (any, error) {
	switch v := v.(type) {
	case map[string]any:
		if ref, ok := v[secretKey]; ok {
			ref, isString := ref.(string)
			if len(v) != 1 || !isString || ref == "" {
				return nil, fmt.Errorf("%s: a secret reference must have the form {%s: <path>}", path, secretKey)
			}
			secret, err := read(ref)
			if err != nil {
				return nil, fmt.Errorf("%s: reading secret: %w", path, err)
			}
			return secret, nil
		}
		for key, elem := range v {
			resolved, err := resolveSecrets(elem, joinPath(path, key), read)
			if err != nil {
				return nil, err
			}
			v[key] = resolved
		}
	case []any:
		for i, elem := range v {
			resolved, err := resolveSecrets(elem, fmt.Sprintf("%s[%d]", path, i), read)
			if err != nil {
				return nil, err
			}
			v[i] = resolved
		}
	}
	return v, nil
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
