/*
   Copyright 2026 Docker Compose CLI authors

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

package compose

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/sirupsen/logrus"
)

// podmanFileReferenceBinds returns legacy bind strings for a service's secrets
// and configs. Podman needs them because it cannot copy files into a container
// that is not running yet, and its SELinux enforcement needs a `z` relabel
// option that structured mounts cannot express. Content is materialized into a
// compose-owned temp file, so user files are never relabeled.
func podmanFileReferenceBinds(p types.Project, s types.ServiceConfig) ([]string, error) {
	binds := make([]string, 0, len(s.Secrets)+len(s.Configs))
	targets := map[string]struct{}{}
	add := func(mountType mountType, name, target string, source types.FileObjectConfig) error {
		path, err := podmanFileReferenceSource(p, mountType, name, source)
		if err != nil {
			return err
		}
		if _, found := targets[target]; found {
			return nil
		}
		targets[target] = struct{}{}
		binds = append(binds, fmt.Sprintf("%s:%s:ro,z", path, target))
		return nil
	}

	for _, secret := range s.Secrets {
		target := secret.Target
		if target == "" {
			target = "/run/secrets/" + secret.Source
		} else if !isAbsTarget(target) {
			target = "/run/secrets/" + target
		}
		source, found := p.Secrets[secret.Source]
		if !found {
			continue
		}
		if err := validatePodmanFileReference(types.FileObjectConfig(source)); err != nil {
			return nil, err
		}
		if err := add(secretMount, secret.Source, target, types.FileObjectConfig(source)); err != nil {
			return nil, err
		}
	}

	for _, config := range s.Configs {
		target := config.Target
		if target == "" {
			target = "/" + config.Source
		} else if !isAbsTarget(target) {
			target = "/" + target
		}
		source, found := p.Configs[config.Source]
		if !found {
			continue
		}
		if err := validatePodmanFileReference(types.FileObjectConfig(source)); err != nil {
			return nil, err
		}
		if err := add(configMount, config.Source, target, types.FileObjectConfig(source)); err != nil {
			return nil, err
		}
	}
	return binds, nil
}

func validatePodmanFileReference(source types.FileObjectConfig) error {
	if source.External {
		return fmt.Errorf("unsupported external file reference %s", source.Name)
	}
	if source.Driver != "" {
		return fmt.Errorf("Docker Compose does not support driver for %s", source.Name) //nolint:staticcheck
	}
	if source.TemplateDriver != "" {
		return fmt.Errorf("Docker Compose does not support template_driver for %s", source.Name) //nolint:staticcheck
	}
	return nil
}

func podmanFileReferenceSource(p types.Project, mountType mountType, name string, source types.FileObjectConfig) (string, error) {
	content := source.Content
	if content == "" && source.Environment != "" {
		var found bool
		content, found = p.Environment[source.Environment]
		if !found {
			return "", fmt.Errorf("environment variable %q required by %s %q is not set", source.Environment, mountType, name)
		}
	}
	if content == "" && source.File != "" {
		path := source.File
		if !filepath.IsAbs(path) {
			path = filepath.Join(p.WorkingDir, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				return "", err
			}
			logrus.Warnf("%s file %s does not exist", mountType, source.Name)
		}
		content = string(data)
	}
	return podmanFileReferenceFile(p.Name, mountType, name, content)
}
