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
