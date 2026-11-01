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
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
)

// containerLinks resolves Docker-style links for the container being created.
// Podman rejects them, so it gets no links; the aliases are published on the
// link targets instead (see podmanLinkAliases).
func (s *composeService) containerLinks(ctx context.Context, projectName string, service types.ServiceConfig, number int) ([]string, error) {
	if isPodman() {
		return nil, nil
	}
	return s.getLinks(ctx, projectName, service, number)
}

// podmanLinkAliases emulates Docker's legacy `links` on Podman, which removed
// container links and rejects HostConfig.Links. The link alias is published on
// the target's endpoint instead, making it resolvable by every container on
// the shared network (a superset of Docker's per-source /etc/hosts entry).
func podmanLinkAliases(p *types.Project, service types.ServiceConfig, networkKey string) []string {
	var aliases []string
	for _, source := range p.Services {
		if source.NetworkMode != "" || service.NetworkMode != "" {
			continue
		}
		if !serviceOnNetwork(source, networkKey) || !serviceOnNetwork(service, networkKey) {
			continue
		}
		for _, rawLink := range source.Links {
			target, alias, ok := strings.Cut(rawLink, ":")
			if !ok {
				target, alias = rawLink, rawLink
			}
			if target == service.Name {
				aliases = append(aliases, alias)
			}
		}
	}
	slices.Sort(aliases)
	return slices.Compact(aliases)
}

func serviceOnNetwork(service types.ServiceConfig, networkKey string) bool {
	if len(service.Networks) == 0 {
		return networkKey == "default"
	}
	_, ok := service.Networks[networkKey]
	return ok
}
