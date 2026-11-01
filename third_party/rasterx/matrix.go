// Implements SVG style matrix transformations.
// https://developer.mozilla.org/en-US/docs/Web/SVG/Attribute/transform
// Copyright 2018 All rights reserved.

package rasterx

import (
	"github.com/stubbedev/lys-lib/third_party/gomath"
	"golang.org/x/image/math/fixed"
)

// Matrix2D represents an SVG style matrix
type Matrix2D struct {
	A, B, C, D, E, F float64
}

// matrix3 is a full 3x3 float64 matrix
// used for inverting
type matrix3 [9]float64

func otherPair(i int) (a, b int) {
	switch i {
	case 0:
		a, b = 1, 2
	case 1:
		a, b = 0, 2
	case 2:
		a, b = 0, 1
	}
	return
}

func (m *matrix3) coFact(i, j int) float64 {
	ai, bi := otherPair(i)
	aj, bj := otherPair(j)
	a, b, c, d := m[ai+aj*3], m[bi+bj*3], m[ai+bj*3], m[bi+aj*3]
	return float64(a*b) - float64(c*d)
}

func (m *matrix3) Invert() *matrix3 {
	var cofact matrix3
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			sign := float64(1 - (i+j%2)%2*2) // "checkerboard of minuses" grid
			cofact[i+j*3] = m.coFact(i, j) * sign
		}
	}
	deteriminate := float64(m[0]*cofact[0]) + float64(m[1]*cofact[1]) + float64(m[2]*cofact[2])

	// transpose cofact
	for i := 0; i < 2; i++ {
		for j := i + 1; j < 3; j++ {
			cofact[i+j*3], cofact[j+i*3] = cofact[j+i*3], cofact[i+j*3]
		}
	}
	for i := 0; i < 9; i++ {
		cofact[i] /= deteriminate
	}
	return &cofact
}

// Invert returns the inverse matrix
func (a Matrix2D) Invert() Matrix2D {
	n := &matrix3{a.A, a.C, a.E, a.B, a.D, a.F, 0, 0, 1}
	n = n.Invert()
	return Matrix2D{A: n[0], C: n[1], E: n[2], B: n[3], D: n[4], F: n[5]}
}

// Mult returns a*b
func (a Matrix2D) Mult(b Matrix2D) Matrix2D {
	return Matrix2D{
		A: float64(a.A*b.A) + float64(a.C*b.B),
		B: float64(a.B*b.A) + float64(a.D*b.B),
		C: float64(a.A*b.C) + float64(a.C*b.D),
		D: float64(a.B*b.C) + float64(a.D*b.D),
		E: float64(a.A*b.E) + float64(a.C*b.F) + a.E,
		F: float64(a.B*b.E) + float64(a.D*b.F) + a.F}
}

// Identity is the identity matrix
var Identity = Matrix2D{1, 0, 0, 1, 0, 0}

// TFixed transforms a fixed.Point26_6 by the matrix
func (a Matrix2D) TFixed(x fixed.Point26_6) (y fixed.Point26_6) {
	y.X = fixed.Int26_6((float64(float64(x.X)*a.A) + float64(float64(x.Y)*a.C)) + float64(a.E*64))
	y.Y = fixed.Int26_6((float64(float64(x.X)*a.B) + float64(float64(x.Y)*a.D)) + float64(a.F*64))
	return
}

// Transform multiples the input vector by matrix m and outputs the results vector
// components.
func (a Matrix2D) Transform(x1, y1 float64) (x2, y2 float64) {
	x2 = float64(x1*a.A) + float64(y1*a.C) + a.E
	y2 = float64(x1*a.B) + float64(y1*a.D) + a.F
	return
}

// TransformVector is a modidifed version of Transform that ignores the
// translation components.
func (a Matrix2D) TransformVector(x1, y1 float64) (x2, y2 float64) {
	x2 = float64(x1*a.A) + float64(y1*a.C)
	y2 = float64(x1*a.B) + float64(y1*a.D)
	return
}

// Scale matrix in x and y dimensions
func (a Matrix2D) Scale(x, y float64) Matrix2D {
	return a.Mult(Matrix2D{
		A: x,
		B: 0,
		C: 0,
		D: y,
		E: 0,
		F: 0})
}

// SkewY skews the matrix in the Y dimension
func (a Matrix2D) SkewY(theta float64) Matrix2D {
	return a.Mult(Matrix2D{
		A: 1,
		B: gomath.Tan(theta),
		C: 0,
		D: 1,
		E: 0,
		F: 0})
}

// SkewX skews the matrix in the X dimension
func (a Matrix2D) SkewX(theta float64) Matrix2D {
	return a.Mult(Matrix2D{
		A: 1,
		B: 0,
		C: gomath.Tan(theta),
		D: 1,
		E: 0,
		F: 0})
}

// Translate translates the matrix to the x , y point
func (a Matrix2D) Translate(x, y float64) Matrix2D {
	return a.Mult(Matrix2D{
		A: 1,
		B: 0,
		C: 0,
		D: 1,
		E: x,
		F: y})
}

// Rotate rotate the matrix by theta
func (a Matrix2D) Rotate(theta float64) Matrix2D {
	return a.Mult(Matrix2D{
		A: gomath.Cos(theta),
		B: gomath.Sin(theta),
		C: -gomath.Sin(theta),
		D: gomath.Cos(theta),
		E: 0,
		F: 0})
}

// MatrixAdder is an adder that applies matrix M to all points
type MatrixAdder struct {
	Adder
	M Matrix2D
}

// Reset sets the matrix M to identity
func (t *MatrixAdder) Reset() {
	t.M = Identity
}

// Start starts a new path
func (t *MatrixAdder) Start(a fixed.Point26_6) {
	t.Adder.Start(t.M.TFixed(a))
}

// Line adds a linear segment to the current curve.
func (t *MatrixAdder) Line(b fixed.Point26_6) {
	t.Adder.Line(t.M.TFixed(b))
}

// QuadBezier adds a quadratic segment to the current curve.
func (t *MatrixAdder) QuadBezier(b, c fixed.Point26_6) {
	t.Adder.QuadBezier(t.M.TFixed(b), t.M.TFixed(c))
}

// CubeBezier adds a cubic segment to the current curve.
func (t *MatrixAdder) CubeBezier(b, c, d fixed.Point26_6) {
	t.Adder.CubeBezier(t.M.TFixed(b), t.M.TFixed(c), t.M.TFixed(d))
}
