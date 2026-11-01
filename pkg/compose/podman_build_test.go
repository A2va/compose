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
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"gotest.tools/v3/assert"

	"github.com/docker/compose/v5/pkg/api"
)

func TestPodmanBuildArgsLabelChangesWithArgs(t *testing.T) {
	original := map[string]string{"keep": "me"}
	args := types.MappingWithEquals{"A": ptr("one"), "B": ptr("two")}
	labels := withPodmanBuildArgsLabel(original, args)

	assert.Equal(t, labels["keep"], "me")
	assert.Assert(t, labels[podmanBuildArgsLabel] != "")
	assert.Equal(t, len(original), 1, "the caller's labels must not be mutated")

	same := withPodmanBuildArgsLabel(nil, types.MappingWithEquals{"B": ptr("two"), "A": ptr("one")})
	assert.Equal(t, labels[podmanBuildArgsLabel], same[podmanBuildArgsLabel], "hash must not depend on map order")

	changed := withPodmanBuildArgsLabel(nil, types.MappingWithEquals{"A": ptr("one"), "B": ptr("other")})
	assert.Assert(t, labels[podmanBuildArgsLabel] != changed[podmanBuildArgsLabel])
}

func ptr(s string) *string {
	return &s
}

func TestImageBuildOptionsCarryComposeLabels(t *testing.T) {
	service := types.ServiceConfig{
		Name: "web",
		WorkloadSpec: types.WorkloadSpec{
			Build: &types.BuildConfig{
				Context: ".",
				Labels:  types.Labels{"custom": "value"},
			},
		},
	}
	project := &types.Project{
		Name:     "demo",
		Services: types.Services{"web": service},
	}

	options := imageBuildOptions(nil, project, service, api.BuildOptions{}, true)

	assert.Equal(t, options.Labels["custom"], "value")
	assert.Equal(t, options.Labels[api.ProjectLabel], "demo")
	assert.Equal(t, options.Labels[api.ServiceLabel], "web")
	assert.Equal(t, options.Labels[api.VersionLabel], api.ComposeVersion)

	unstable := imageBuildOptions(nil, project, service, api.BuildOptions{}, false)
	assert.Equal(t, unstable.Labels["custom"], "value")
	_, hasProject := unstable.Labels[api.ProjectLabel]
	assert.Assert(t, !hasProject, "Podman <4.9 must not get labels whose order breaks its build cache")
}

func TestPodmanDockerfileWithLabels(t *testing.T) {
	labels := map[string]string{"b": "two", "a": "one"}

	assert.Equal(t,
		podmanDockerfileWithLabels("FROM alpine\nCMD [\"true\"]\n", labels, ""),
		"FROM alpine\nCMD [\"true\"]\nLABEL \"a\"=\"one\"\nLABEL \"b\"=\"two\"\n")

	multiStage := "FROM alpine AS base\nRUN true\nFROM scratch AS final\nCOPY --from=base /x /x\n"
	assert.Equal(t,
		podmanDockerfileWithLabels(multiStage, labels, "base"),
		"FROM alpine AS base\nRUN true\nLABEL \"a\"=\"one\"\nLABEL \"b\"=\"two\"\nFROM scratch AS final\nCOPY --from=base /x /x\n")
	assert.Equal(t,
		podmanDockerfileWithLabels(multiStage, labels, "final"),
		"FROM alpine AS base\nRUN true\nFROM scratch AS final\nCOPY --from=base /x /x\nLABEL \"a\"=\"one\"\nLABEL \"b\"=\"two\"\n")
}

func TestPodmanRewriteServiceContexts(t *testing.T) {
	dockerfile := "FROM base\nCOPY --from=base /out /out\nRUN echo FROM base\n"
	rewritten := podmanRewriteServiceContexts(dockerfile, map[string]string{"base": "project-base"})
	assert.Equal(t, rewritten, "FROM project-base\nCOPY --from=project-base /out /out\nRUN echo FROM base\n")

	assert.Equal(t, podmanRewriteServiceContexts(dockerfile, nil), dockerfile)
}
