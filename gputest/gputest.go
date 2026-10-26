// Package gputest picks the Vulkan driver the GPU tests run on, from
// the environment: LYS_UI_TEST_GPU=lavapipe runs them on Mesa's CPU
// driver (CI, no GPU needed), whose ICD file LYS_UI_TEST_LAVAPIPE names
// (the dev shell sets it); any other value runs them on the host's
// device; unset, they run where a device is found and skip elsewhere.
package gputest

import (
	"errors"
	"fmt"
	"os"
	"testing"
)

// Setup points the Vulkan loader, through setenv, at the driver
// LYS_UI_TEST_GPU asks for, and at the validation layer when
// LYS_UI_TEST_VULKAN_LAYERS names the directory of its manifest (the
// dev shell does): any error the layer finds stops the test with a
// SIGTRAP at the offending call, after logging it. required is whether
// a missing device fails the test rather than skipping it.
func Setup(setenv func(key, value string) error) (required bool, err error) {
	mode := os.Getenv("LYS_UI_TEST_GPU")
	var env [][2]string
	if mode == "lavapipe" {
		icd := os.Getenv("LYS_UI_TEST_LAVAPIPE")
		if icd == "" {
			return true, errors.New("LYS_UI_TEST_GPU=lavapipe without LYS_UI_TEST_LAVAPIPE (the dev shell sets it)")
		}
		env = append(env, [2]string{"VK_DRIVER_FILES", icd})
	}
	if layers := os.Getenv("LYS_UI_TEST_VULKAN_LAYERS"); layers != "" {
		env = append(env,
			[2]string{"VK_ADD_LAYER_PATH", layers},
			[2]string{"VK_LOADER_LAYERS_ENABLE", "*validation"},
			[2]string{"VK_KHRONOS_VALIDATION_DEBUG_ACTION", "VK_DBG_LAYER_ACTION_LOG_MSG,VK_DBG_LAYER_ACTION_BREAK"},
		)
	}
	for _, kv := range env {
		if err := setenv(kv[0], kv[1]); err != nil {
			return mode != "", fmt.Errorf("gputest: setting %s: %w", kv[0], err)
		}
	}
	return mode != "", nil
}

// SetupTest is Setup for t alone, through t.Setenv.
func SetupTest(t testing.TB) (required bool, err error) {
	return Setup(func(key, value string) error {
		t.Setenv(key, value)
		return nil
	})
}

// Enabled reports whether LYS_UI_TEST_GPU asks for the GPU tests, which
// the suites outside package gpu run only then.
func Enabled() bool { return os.Getenv("LYS_UI_TEST_GPU") != "" }
