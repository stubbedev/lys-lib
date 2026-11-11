// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !appengine && gc && !noasm

package vector

import "golang.org/x/sys/cpu"

// lys-lib patch: read on use, where upstream copied it when the package
// was imported.
func haveAccumulateSIMD() bool { return cpu.X86.HasSSE41 }

//go:noescape
func fixedAccumulateOpOverSIMD(dst []uint8, src []uint32)

//go:noescape
func fixedAccumulateOpSrcSIMD(dst []uint8, src []uint32)

//go:noescape
func fixedAccumulateMaskSIMD(buf []uint32)

//go:noescape
func floatingAccumulateOpOverSIMD(dst []uint8, src []float32)

//go:noescape
func floatingAccumulateOpSrcSIMD(dst []uint8, src []float32)

//go:noescape
func floatingAccumulateMaskSIMD(dst []uint32, src []float32)
