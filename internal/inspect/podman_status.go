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

package inspect

import (
	"fmt"
	"strings"

	"github.com/moby/moby/api/types/container"
)

// podmanContainerStatus restores the health suffix Docker includes in the
// /containers/json status field. Podman only exposes the health state through
// inspect, so without this the "STATUS" column of `compose ps` drops it.
func podmanContainerStatus(status string, health container.HealthStatus) string {
	var label string
	switch health {
	case container.Healthy, container.Unhealthy:
		label = string(health)
	case container.Starting:
		label = "health: " + string(health)
	default:
		return status
	}
	if strings.Contains(status, "("+label+")") {
		return status
	}
	return fmt.Sprintf("%s (%s)", status, label)
}
