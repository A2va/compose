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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/docker/compose/v5/pkg/api"
)

const podmanBuildArgsLabel = "com.docker.compose.build.args"

// withPodmanBuildArgsLabel fingerprints the build args into the image config.
// buildah ignores a changed arg for a cacheable step, so without this the new
// image keeps the old ID and compose cannot tell that `up` must recreate.
func withPodmanBuildArgsLabel(labels map[string]string, args types.MappingWithEquals) map[string]string {
	if labels == nil {
		labels = map[string]string{}
	} else {
		labels = maps.Clone(labels)
	}
	labels[podmanBuildArgsLabel] = podmanBuildArgsHash(args)
	return labels
}

func podmanBuildArgsHash(args types.MappingWithEquals) string {
	h := sha256.New()
	for _, k := range slices.Sorted(maps.Keys(args)) {
		v := "<nil>"
		if args[k] != nil {
			v = *args[k]
		}
		_, _ = h.Write([]byte(k + "=" + v + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// podmanDockerfileLabels are the standard Compose image labels for engines
// whose build-request label order is not stable: writing them into the
// Dockerfile keeps the image ID identical across rebuilds.
func podmanDockerfileLabels(project *types.Project, service types.ServiceConfig) map[string]string {
	return map[string]string{
		api.ProjectLabel: project.Name,
		api.ServiceLabel: service.Name,
		api.VersionLabel: api.ComposeVersion,
	}
}

// podmanDockerfileWithLabels appends deterministic LABEL instructions to the
// target stage (or the final stage) of dockerfile.
func podmanDockerfileWithLabels(dockerfile string, labels map[string]string, target string) string {
	if len(labels) == 0 {
		return dockerfile
	}
	labelLines := make([]string, 0, len(labels))
	for _, key := range slices.Sorted(maps.Keys(labels)) {
		labelLines = append(labelLines, fmt.Sprintf("LABEL %q=%q", key, labels[key]))
	}

	lines := strings.Split(dockerfile, "\n")
	insertAt := len(lines)
	if target != "" {
		inTarget := false
		for i, line := range lines {
			fields := strings.Fields(line)
			if len(fields) < 2 || !strings.EqualFold(fields[0], "FROM") {
				continue
			}
			if inTarget {
				insertAt = i
				break
			}
			for j := 2; j+1 < len(fields); j++ {
				if strings.EqualFold(fields[j], "AS") && strings.EqualFold(fields[j+1], target) {
					inTarget = true
					break
				}
			}
		}
	}

	if insertAt == len(lines) && lines[len(lines)-1] == "" {
		// Keep the trailing newline before the injected instructions.
		insertAt--
	}
	rewritten := make([]string, 0, len(lines)+len(labelLines))
	rewritten = append(rewritten, lines[:insertAt]...)
	rewritten = append(rewritten, labelLines...)
	rewritten = append(rewritten, lines[insertAt:]...)
	return strings.Join(rewritten, "\n")
}

// podmanServiceContextsOnly reports whether every additional context is a
// service reference, which the classic builder can emulate by using the image
// that service's build produces.
func podmanServiceContextsOnly(contexts map[string]string) bool {
	for _, context := range contexts {
		if _, ok := strings.CutPrefix(context, types.ServicePrefix); !ok {
			return false
		}
	}
	return true
}

// podmanResolveServiceContexts maps named service contexts to the image tag
// the classic builder produces for the referenced service.
func podmanResolveServiceContexts(project *types.Project, service types.ServiceConfig) map[string]string {
	images := map[string]string{}
	for name, context := range service.Build.AdditionalContexts {
		target, ok := strings.CutPrefix(context, types.ServicePrefix)
		if !ok {
			continue
		}
		if targetService, ok := project.Services[target]; ok {
			images[name] = api.GetImageNameOrDefault(targetService, project.Name)
		}
	}
	return images
}

func podmanRewriteServiceContexts(dockerfile string, images map[string]string) string {
	if len(images) == 0 {
		return dockerfile
	}
	lines := strings.Split(dockerfile, "\n")
	for i, line := range lines {
		fields := strings.Fields(line)
		changed := false
		if len(fields) >= 2 && strings.EqualFold(fields[0], "FROM") {
			// skip FROM flags such as --platform=…
			imageIndex := 1
			for imageIndex < len(fields) && strings.HasPrefix(fields[imageIndex], "--") {
				imageIndex++
			}
			if imageIndex < len(fields) {
				if image, ok := images[fields[imageIndex]]; ok {
					fields[imageIndex] = image
					changed = true
				}
			}
		}
		for j, field := range fields {
			if context, ok := strings.CutPrefix(field, "--from="); ok {
				if image, ok := images[context]; ok {
					fields[j] = "--from=" + image
					changed = true
				}
			}
		}
		if changed {
			lines[i] = strings.Join(fields, " ")
		}
	}
	return strings.Join(lines, "\n")
}
