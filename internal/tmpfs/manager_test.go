package tmpfs

import "testing"

func TestPrepareValidation(t *testing.T) {
	if err := (Manager{}).Prepare("relative", 512); err == nil {
		t.Fatal("relative path accepted")
	}
	if err := (Manager{}).Prepare("/tmp/conan-manager-test", 64); err == nil {
		t.Fatal("small size accepted")
	}
}
