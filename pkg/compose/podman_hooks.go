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
	"context"
	"fmt"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// podmanSkipsPreStopHooks reports whether ctr cannot run a pre_stop hook.
// Podman rejects an exec in a stopped or paused container with a plain error,
// while Docker answers with a conflict, which upstream uses to skip the hook
// and still stop or restart the container.
func podmanSkipsPreStopHooks(ctr container.Summary) bool {
	return isPodman() && ctr.State != container.StateRunning
}

// podmanWaitPreStart polls the hook runner until it stops and returns its exit
// code. Podman 4.x rootful can miss the exit event of a short-lived container,
// leaving ContainerWait pending until the caller's deadline; state polling has
// no such race.
func (s *composeService) podmanWaitPreStart(ctx context.Context, serviceName string, index int, containerID string) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		res, err := s.apiClient().ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
		if err != nil {
			return err
		}
		state := res.Container.State
		if state == nil {
			return fmt.Errorf("service %q pre_start[%d]: hook container %s has no state", serviceName, index, containerID)
		}
		// Podman 4.3 can report a finished container as "stopped" before it
		// settles on "exited".
		if state.Status == container.StateExited || state.Status == container.StateDead || string(state.Status) == "stopped" {
			return preStartResultErr(serviceName, index, container.WaitResponse{StatusCode: int64(state.ExitCode)})
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
