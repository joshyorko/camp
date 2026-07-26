//go:build linux && kubernetes_evidence

package integration

import "testing"

func TestKubernetesLifecycleVertical(t *testing.T) {
	runKubernetesLifecycleVertical(t)
}
