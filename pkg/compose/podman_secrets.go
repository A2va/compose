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
	"path"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/mount"
)

// podmanSecretVolume mounts an anonymous volume at /run/secrets when a secret
// targets that directory. Podman mounts a tmpfs there, which shadows files
// written to a stopped container; archive copies do write through volumes, so
// the uid/gid/mode declared on the secret are preserved.
func podmanSecretVolume(service types.ServiceConfig, mounts []mount.Mount) []mount.Mount {
	for _, m := range mounts {
		if path.Clean(m.Target) == "/run/secrets" {
			return nil
		}
	}
	for _, secret := range service.Secrets {
		target := secret.Target
		if target == "" {
			target = "/run/secrets/" + secret.Source
		} else if !isAbsTarget(target) {
			target = "/run/secrets/" + target
		}
		target = path.Clean(target)
		if target == "/run/secrets" || strings.HasPrefix(target, "/run/secrets/") {
			return []mount.Mount{{Type: mount.TypeVolume, Target: "/run/secrets"}}
		}
	}
	return nil
}
