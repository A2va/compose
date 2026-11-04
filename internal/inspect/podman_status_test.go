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
	"testing"

	"github.com/moby/moby/api/types/container"
	"gotest.tools/v3/assert"
)

func TestPodmanContainerStatus(t *testing.T) {
	tests := []struct {
		name   string
		status string
		health container.HealthStatus
		want   string
	}{
		{
			name:   "healthy",
			status: "Up 4 seconds",
			health: container.Healthy,
			want:   "Up 4 seconds (healthy)",
		},
		{
			name:   "unhealthy",
			status: "Up 4 seconds",
			health: container.Unhealthy,
			want:   "Up 4 seconds (unhealthy)",
		},
		{
			name:   "starting",
			status: "Up 4 seconds",
			health: container.Starting,
			want:   "Up 4 seconds (health: starting)",
		},
		{
			name:   "no healthcheck",
			status: "Up 4 seconds",
			health: container.NoHealthcheck,
			want:   "Up 4 seconds",
		},
		{
			name:   "already formatted",
			status: "Up 4 seconds (healthy)",
			health: container.Healthy,
			want:   "Up 4 seconds (healthy)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, podmanContainerStatus(tt.status, tt.health), tt.want)
		})
	}
}
