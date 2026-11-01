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
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/sirupsen/logrus"
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

// applyPodmanLinkHosts writes legacy links into the source container's
// /etc/hosts after start. Podman's default bridge has no DNS, and it assigns
// the target's address at start, too late for HostConfig.ExtraHosts.
func (s *composeService) applyPodmanLinkHosts(ctx context.Context, projectName string, service types.ServiceConfig, sourceID string) error {
	if !isPodman() || (service.NetworkMode != "bridge" && service.NetworkMode != "default") || len(service.Links) == 0 {
		return nil
	}
	targets := map[string]Containers{}
	for _, rawLink := range service.Links {
		target, _, _ := strings.Cut(rawLink, ":")
		if _, ok := targets[target]; ok {
			continue
		}
		containers, err := s.getContainers(ctx, projectName, oneOffExclude, true, target)
		if err != nil {
			return err
		}
		targets[target] = containers
	}
	hosts := podmanLinkHostEntries(service.Links, targets)
	if len(hosts) == 0 {
		return nil
	}
	cmd := append([]string{"sh", "-c", `for e in "$@"; do h="${e%%:*}"; ip="${e#*:}"; grep -q " $h$" /etc/hosts || echo "$ip $h" >> /etc/hosts; done`, "sh"}, hosts...)
	exec, err := s.apiClient().ExecCreate(ctx, sourceID, client.ExecCreateOptions{Cmd: cmd})
	if err != nil {
		// The image may be shell-less; keep the current no-DNS behavior
		// rather than failing the start over a best-effort emulation.
		logrus.Debugf("unable to inject links into %s: %v", sourceID, err)
		return nil
	}
	if _, err := s.apiClient().ExecStart(ctx, exec.ID, client.ExecStartOptions{Detach: true}); err != nil {
		logrus.Debugf("unable to run link injection in %s: %v", sourceID, err)
	}
	return nil
}

func podmanLinkHostEntries(links []string, targets map[string]Containers) []string {
	var hosts []string
	for _, rawLink := range links {
		target, alias, ok := strings.Cut(rawLink, ":")
		if !ok {
			alias = target
		}
		for _, ctr := range targets[target] {
			ip := podmanContainerIP(ctr)
			if ip == "" {
				continue
			}
			hosts = append(hosts, target+":"+ip)
			if alias != target {
				hosts = append(hosts, alias+":"+ip)
			}
		}
	}
	slices.Sort(hosts)
	return slices.Compact(hosts)
}

func podmanContainerIP(ctr container.Summary) string {
	if ctr.NetworkSettings == nil {
		return ""
	}
	for _, endpoint := range ctr.NetworkSettings.Networks {
		if endpoint != nil && endpoint.IPAddress.IsValid() {
			return endpoint.IPAddress.String()
		}
	}
	return ""
}
