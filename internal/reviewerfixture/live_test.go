package reviewerfixture

import "testing"

func TestNewLiveTargetRequiresGateway(t *testing.T) {
	t.Parallel()

	if _, err := NewLiveTarget(nil); err == nil {
		t.Fatal("nil gateway was accepted")
	}
}
