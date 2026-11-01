# Go 1.27.3 math, patched for lys-lib

A copy of the functions of Go 1.27.3's standard `math` package whose results depend
on the architecture (BSD 3-Clause, LICENSE): `Sin`, `Cos`, `Sincos`, `Tan`, `Atan`,
`Atan2`, `Asin`, `Acos`, `Exp`, `Exp2`, `Log`, `Log2`, `Log10`, `Pow`, `Cbrt`, `Hypot`,
`Erf` and `Erfc`, imported as `github.com/stubbedev/lys-lib/third_party/gomath`. Every
lys package calls these instead of `math`'s, which `.golangci.yml` enforces. `Sqrt`,
`Floor`, `Abs`, `Mod` and the rest of `math` are exact, so they stay in `math`.

## Why the standard library is not enough

`math`'s results differ between architectures in two ways:

- Its portable code is ordinary Go, and on arm64 the compiler fuses its `x*y + z`
  into FMAs. Go's own tests allow for that with a tolerance, so `math.Sin` on arm64
  is not `math.Sin` on amd64.
- `Exp` is assembly on amd64 and on arm64, and `Log` and `Hypot` on amd64. amd64's
  `Exp` also picks an FMA path at run time from the CPU, so its results differ
  between x86 CPUs too. `Pow` and `Erfc` call `Exp`.

## Patches

### Portable code only

The `haveArch*` branches to assembly are gone, and `math`'s exact helpers are called
through `math`.

### Products round the same on every architecture

As in `third_party/rasterx`, each product that reaches an addition or a subtraction
is converted first. On amd64, against `math`: 20000 inputs per function gave the same
bits for `Sin`, `Cos`, `Tan`, `Atan`, `Asin`, `Acos`, `Exp2`, `Log`, `Log2`, `Log10`,
`Cbrt` and `Hypot`. `Exp` differs on 16% of inputs, `Pow` on 12% and `Erfc` on 7%, by
the last bits, because the assembly `Exp` is gone. Pinned by `gomath_test.go`, whose
single table of hashes holds on every architecture.
