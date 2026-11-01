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
	"net/netip"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"gotest.tools/v3/assert"
)

func TestPodmanLinkHostEntries(t *testing.T) {
	target := container.Summary{
		NetworkSettings: &container.NetworkSettingsSummary{
			Networks: map[string]*network.EndpointSettings{
				"podman": {IPAddress: netip.MustParseAddr("10.88.0.2")},
			},
		},
	}
	hosts := podmanLinkHostEntries(
		[]string{"db", "db:database", "missing"},
		map[string]Containers{"db": {target}},
	)
	assert.DeepEqual(t, hosts, []string{"database:10.88.0.2", "db:10.88.0.2"})
}
