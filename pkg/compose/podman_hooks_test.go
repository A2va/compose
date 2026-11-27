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
	"github.com/moby/moby/client"
	"go.uber.org/mock/gomock"
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

func TestPodmanWaitPreStartPollsUntilTheHookExits(t *testing.T) {
	tested, apiClient := newPreStartTestService(t)
	running := client.ContainerInspectResult{Container: container.InspectResponse{
		State: &container.State{Status: container.StateRunning, Running: true},
	}}
	exited := client.ContainerInspectResult{Container: container.InspectResponse{
		State: &container.State{Status: container.StateExited, ExitCode: 17},
	}}
	gomock.InOrder(
		apiClient.EXPECT().ContainerInspect(gomock.Any(), "hook-1", gomock.Any()).Return(running, nil),
		apiClient.EXPECT().ContainerInspect(gomock.Any(), "hook-1", gomock.Any()).Return(exited, nil),
	)
	err := tested.podmanWaitPreStart(t.Context(), "web", 0, "hook-1")
	assert.ErrorContains(t, err, "exited with code 17")
}
