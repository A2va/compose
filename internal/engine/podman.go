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

package engine

import (
	"os"
	"strings"
)

// podmanProfile records that the engine was resolved to Podman at startup,
// before docker/cli initializes its client.
var podmanProfile bool

// SetPodmanProfile records whether this invocation resolves to Podman.
func SetPodmanProfile(on bool) { podmanProfile = on }

// IsPodman reports whether this invocation targets Podman. `podman compose`
// exports DOCKER_HOST pointing at the Podman socket, so the socket path also
// serves as detection for invocations that skip our startup defaults.
func IsPodman() bool {
	return podmanProfile || strings.Contains(os.Getenv("DOCKER_HOST"), "podman")
}
