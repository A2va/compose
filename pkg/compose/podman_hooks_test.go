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

	"github.com/moby/moby/api/types/container"
	"gotest.tools/v3/assert"
)

func TestPodmanSkipsPreStopHooksOutsideRunningState(t *testing.T) {
	resetPodmanState(t)
	t.Setenv("DOCKER_HOST", "unix:///run/podman/podman.sock")

	assert.Check(t, podmanSkipsPreStopHooks(container.Summary{State: container.StateCreated}))
	assert.Check(t, podmanSkipsPreStopHooks(container.Summary{State: container.StateExited}))
	assert.Check(t, podmanSkipsPreStopHooks(container.Summary{State: container.StatePaused}))
	assert.Check(t, !podmanSkipsPreStopHooks(container.Summary{State: container.StateRunning}))

	t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
	assert.Check(t, !podmanSkipsPreStopHooks(container.Summary{State: container.StateExited}))
}
