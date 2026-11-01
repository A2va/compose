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
	"maps"
	"slices"

	"github.com/compose-spec/compose-go/v2/types"
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
