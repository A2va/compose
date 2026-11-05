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
	"slices"
	"strings"

	"github.com/sirupsen/logrus"
)

// podmanContainerRemoved reports the error Podman returns when a container is
// removed between the inspect that opened a log stream and the log request
// itself. It answers with a generic 500 ("no container with ID ... found in
// database"), not the 404 errdefs.IsNotFound recognizes, so callers that treat
// a vanished container as already finished need this too.
func podmanContainerRemoved(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no container with ID") &&
		strings.Contains(msg, "found in database")
}

// podmanLogDriver maps Docker-only drivers to a Podman-supported one. Podman
// rejects the container create otherwise; k8s-file keeps the output locally.
func podmanLogDriver(driver string) string {
	if slices.Contains([]string{"", "k8s-file", "journald", "none", "passthrough"}, driver) {
		return driver
	}
	if driver == "json-file" {
		return "k8s-file"
	}
	logrus.Warnf("Podman does not support log driver %q; falling back to k8s-file (logs stay local)", driver)
	return "k8s-file"
}
