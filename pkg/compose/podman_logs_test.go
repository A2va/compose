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

	"gotest.tools/v3/assert"
)

func TestPodmanLogDriver(t *testing.T) {
	for _, driver := range []string{"", "k8s-file", "journald", "none", "passthrough"} {
		assert.Equal(t, podmanLogDriver(driver), driver)
	}
	assert.Equal(t, podmanLogDriver("json-file"), "k8s-file")
	assert.Equal(t, podmanLogDriver("fluentd"), "k8s-file")
}
