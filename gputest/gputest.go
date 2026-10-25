// Package gputest picks the Vulkan driver the GPU tests run on, from
// the environment: LYS_UI_TEST_GPU=lavapipe runs them on Mesa's CPU
// driver (CI, no GPU needed), whose ICD file LYS_UI_TEST_LAVAPIPE names
// (the dev shell sets it); any other value runs them on the host's
// device; unset, they run where a device is found and skip elsewhere.
package gputest

import (
	"errors"
	"os"
)

// Driver is the ICD file to point VK_DRIVER_FILES at, empty for the
// loader's own choice, and whether a missing device fails the test
// rather than skipping it.
func Driver() (icd string, required bool, err error) {
	mode := os.Getenv("LYS_UI_TEST_GPU")
	if mode != "lavapipe" {
		return "", mode != "", nil
	}
	icd = os.Getenv("LYS_UI_TEST_LAVAPIPE")
	if icd == "" {
		return "", true, errors.New("LYS_UI_TEST_GPU=lavapipe without LYS_UI_TEST_LAVAPIPE (the dev shell sets it)")
	}
	return icd, true, nil
}

// Enabled reports whether LYS_UI_TEST_GPU asks for the GPU tests, which
// the suites outside package gpu run only then.
func Enabled() bool { return os.Getenv("LYS_UI_TEST_GPU") != "" }
