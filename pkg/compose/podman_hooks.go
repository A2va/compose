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
	"github.com/moby/moby/api/types/container"
)

// podmanSkipsPreStopHooks reports whether ctr cannot run a pre_stop hook.
// Podman rejects an exec in a stopped or paused container with a plain error,
// while Docker answers with a conflict, which upstream uses to skip the hook
// and still stop or restart the container.
func podmanSkipsPreStopHooks(ctr container.Summary) bool {
	return isPodman() && ctr.State != container.StateRunning
}
