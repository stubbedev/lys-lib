#version 450
#extension GL_GOOGLE_include_directive : require
#include "prim.glsl"

// The kinds, gpu/prim.go's.
const uint kindRect = 0u;
const uint kindRRect = 1u;
const uint kindRounded = 2u;
const uint kindBorder = 3u;
const uint kindTexture = 4u;
const uint kindMask = 5u;
const uint kindComposite = 6u;
const uint kindStretch = 7u;
const uint kindImage = 8u;
const uint kindGradient = 9u;
const uint kindLine = 10u;
const uint kindArc = 11u;
const uint kindCover = 12u;
const uint kindMatrix = 13u;
const uint kindCrossFade = 14u;
const uint kindMaskLayer = 15u;
const uint kindPathMask = 16u;
const uint kindBoxH = 17u;
const uint kindBoxV = 18u;
const uint kindDownsample = 19u;
const uint kindUpsample = 20u;
const uint kindSilhouette = 21u;
const uint kindShadow = 22u;
const uint kindGrain = 23u;
const uint kindImageTiles = 24u;
const uint kindGradientTiles = 25u;
const uint kindBlend = 26u;
const uint kindRefract = 27u;

// The info.w flags, gpu/prim.go's.
const uint flagRaw = 1u;
const uint flagOpaqueStores = 2u;
const uint flagNoBright = 4u;

layout(set = 1, binding = 0) uniform sampler2D tex;
layout(set = 2, binding = 0) uniform sampler2D tex2;

layout(location = 0) flat in uint vPrim;
layout(location = 0) out vec4 outColor;

// Colors are uvec4(a, r, g, b) in 0..255, premultiplied, as the CPU
// renderer's integer arithmetic holds them.
uvec4 unpack(uint c) {
    return uvec4(c >> 24, (c >> 16) & 255u, (c >> 8) & 255u, c & 255u);
}

uvec4 texel(ivec2 at) {
    vec4 t = texelFetch(tex, at, 0);
    uvec4 c = uvec4(floor(t * 255.0 + 0.5));
    return uvec4(c.a, c.r, c.g, c.b);
}

uvec4 texel2(ivec2 at) {
    vec4 t = texelFetch(tex2, at, 0);
    uvec4 c = uvec4(floor(t * 255.0 + 0.5));
    return uvec4(c.a, c.r, c.g, c.b);
}

// masked is render's layer masking: full coverage keeps the pixel, none
// clears it, the rest scales it.
uvec4 masked(uvec4 c, uint cov) {
    if (cov == 255u) {
        return c;
    }
    if (cov == 0u) {
        return uvec4(0u);
    }
    return c * cov / 255u;
}

uvec4 coverageScale(uvec4 c, uint cov) {
    return c * cov / 255u;
}

uvec4 brighten(uvec4 c, float f) {
    precise vec4 o = floor(vec4(c) * f + 0.5);
    return uvec4(c.x, min(uvec3(c.x), uvec3(o.yzw)));
}

// shade is render.Canvas.blend's source half: brightness, then the
// opacity factor.
uvec4 shade(uvec4 c, uint alpha, float bright, bool noBright) {
    if (!noBright && bright != 0.0 && bright != 1.0) {
        c = brighten(c, bright);
    }
    if (alpha != 255u) {
        c = (c * alpha + 127u) / 255u;
    }
    return c;
}

float coverage(float d) {
    precise float c = clamp(0.5 - d, 0.0, 1.0);
    return c;
}

// sdBox, sdRoundRect and borderSide are render's sdBoxF, sdRoundRect
// and borderSide, step for step in float32.
float sdBox(vec2 p, vec4 b, vec4 rc) {
    bool left = p.x < (b.x + b.z) * 0.5;
    bool top = p.y < (b.y + b.w) * 0.5;
    precise float ex = left ? p.x - b.x : b.z - p.x;
    precise float ey = top ? p.y - b.y : b.w - p.y;
    float rad = left ? (top ? rc.x : rc.w) : (top ? rc.y : rc.z);
    precise float qx = rad - ex;
    precise float qy = rad - ey;
    precise float mx = max(qx, 0.0);
    precise float my = max(qy, 0.0);
    precise float d = sqrt(mx * mx + my * my) + min(max(qx, qy), 0.0) - rad;
    return d;
}

float sdRoundRect(vec2 p, vec4 r, float radius) {
    precise vec2 c = (r.xy + r.zw) * 0.5;
    precise vec2 h = (r.zw - r.xy) * 0.5 - radius;
    precise vec2 q = abs(p - c) - h;
    precise float mx = max(q.x, 0.0);
    precise float my = max(q.y, 0.0);
    precise float d = sqrt(mx * mx + my * my) + min(max(q.x, q.y), 0.0) - radius;
    return d;
}

uint borderSide(vec2 p, vec4 b, vec4 inv) {
    float d[4] = float[4](p.y - b.y, b.z - p.x, b.w - p.y, p.x - b.x);
    float iv[4] = float[4](inv.x, inv.y, inv.z, inv.w);
    uint best = 0u;
    float bestV = 1.0 / 0.0;
    for (uint i = 0u; i < 4u; i++) {
        if (iv[i] == 0.0) {
            continue;
        }
        precise float v = d[i] * iv[i];
        if (v < bestV) {
            best = i;
            bestV = v;
        }
    }
    return best;
}

// cov255 is render's: coverage quantized to 0..255, rounding half up,
// the product rounded before the half is added.
// grainNoise is render's grainNoise: a hashed byte for a pixel offset.
uint grainNoise(ivec2 at) {
    uint h = uint(at.x) * 0x27d4eb2du ^ uint(at.y) * 0x165667b1u;
    h ^= h >> 15;
    h *= 0x85ebca6bu;
    h ^= h >> 13;
    return h & 255u;
}

uint cov255(float cov) {
    precise float q = cov * 255.0;
    precise float r = q + 0.5;
    return uint(floor(r));
}

// covered applies render.Canvas.blendCov's coverage step.
uvec4 covered(uvec4 c, float cov) {
    if (cov <= 0.0) {
        discard;
    }
    if (cov >= 1.0) {
        return c;
    }
    return coverageScale(c, cov255(cov));
}

// stretch maps a box column onto its core's, render.BoxShadow's: the
// core's middle one repeats across a longer box.
int stretch(int b, int full, int core) {
    int mid = core / 2;
    if (b < mid) {
        return b;
    }
    if (b >= mid + full - core) {
        return b - (full - core);
    }
    return mid;
}

// modFloor is render's: x - y*floor(x*invY), the formula both
// renderers fold by, dividing as a multiplication by invY = 1/y.
float modFloor(float x, float y, float invY) {
    precise float q = floor(x * invY);
    precise float r = x - y * q;
    return r;
}

// tileAt is render.TileGeometry.At: px's offset into the tile it falls
// in, folded by modFloor along a repeating axis, and whether a tile
// covers it.
bool tileAt(Prim p, vec2 px, out vec2 uv) {
    precise float u = px.x - p.inner.x;
    precise float v = px.y - p.inner.y;
    if (p.widths.x != 0.0) {
        u = modFloor(u, p.iradii.x, p.iradii.z);
    }
    if (p.widths.y != 0.0) {
        v = modFloor(v, p.iradii.y, p.iradii.w);
    }
    uv = vec2(u, v);
    return u >= 0.0 && u < p.inner.z && v >= 0.0 && v < p.inner.w;
}

// The blend modes' fixed-point units, render's blendK and blendQ.
const int blendK = 65025;
const int blendQ = 4080;

// roundDiv is render's: x / d rounded half away from zero, d > 0.
int roundDiv(int x, int d) {
    return x >= 0 ? (x + d / 2) / d : -((d / 2 - x) / d);
}

int hardLight(int s, int b) {
    if (2 * s <= 255) {
        return b * 2 * s;
    }
    int s2 = 2 * s - 255;
    return (b + s2) * 255 - b * s2;
}

// blendSeparable is render's: a separable mode's B(Cb, Cs) for one
// channel, straight s and b, premultiplied sp and bp under alphas sa
// and ba, in blendK units. Soft-light's table sits in the words at
// p.cols.y.
int blendSeparable(Prim p, uint m, int s, int b, int sp, int sa, int bp, int ba) {
    switch (m) {
    case 1u:
        return s * b;
    case 2u:
        return (s + b) * 255 - s * b;
    case 3u:
        return hardLight(b, s);
    case 4u:
        return min(s, b) * 255;
    case 5u:
        return max(s, b) * 255;
    case 6u: {
        if (bp == 0 || ba == 0) {
            return 0;
        }
        if (sp >= sa) {
            return blendK;
        }
        int d = ba * (sa - sp);
        return 255 * min(255, (bp * sa * 255 + d / 2) / d);
    }
    case 7u: {
        if (ba == 0) {
            return 0;
        }
        if (bp >= ba) {
            return blendK;
        }
        if (sp == 0) {
            return 0;
        }
        int d = ba * sp;
        return 255 * (255 - min(255, ((ba - bp) * sa * 255 + d / 2) / d));
    }
    case 8u:
        return hardLight(s, b);
    case 9u:
        if (2 * s <= 255) {
            return b * 255 - roundDiv((255 - 2 * s) * b * (255 - b), 255);
        }
        return b * 255 + roundDiv((2 * s - 255) * (int(words[p.cols.y + uint(b)]) - b * 255), 255);
    case 10u:
        return abs(b - s) * 255;
    case 11u:
        return (s + b) * 255 - 2 * s * b;
    }
    return s * 255;
}

int lumQ(ivec3 c) {
    return (30 * c.x + 59 * c.y + 11 * c.z + 50) / 100;
}

// setLumQ is render's: c shifted to luminance l, then clipped toward
// it.
ivec3 setLumQ(ivec3 c, int l) {
    c += l - lumQ(c);
    l = lumQ(c);
    int lo = min(c.x, min(c.y, c.z));
    int hi = max(c.x, max(c.y, c.z));
    if (lo < 0 && l != lo) {
        c = l + (c - l) * l / (l - lo);
    }
    if (hi > blendQ && hi != l) {
        c = l + (c - l) * (blendQ - l) / (hi - l);
    }
    return c;
}

int satQ(ivec3 c) {
    return max(c.x, max(c.y, c.z)) - min(c.x, min(c.y, c.z));
}

// setSatQ is render's: small at 0, large at s, mid in proportion, ties
// kept in channel order.
ivec3 setSatQ(ivec3 c, int s) {
    int o0 = 0, o1 = 1, o2 = 2, t;
    if (c[o0] > c[o1]) { t = o0; o0 = o1; o1 = t; }
    if (c[o1] > c[o2]) { t = o1; o1 = o2; o2 = t; }
    if (c[o0] > c[o1]) { t = o0; o0 = o1; o1 = t; }
    ivec3 out_ = ivec3(0);
    int span = c[o2] - c[o0];
    if (span > 0) {
        out_[o1] = (c[o1] - c[o0]) * s / span;
        out_[o2] = s;
    }
    return out_;
}

ivec3 blendNonSeparable(uint m, ivec3 s, ivec3 b) {
    switch (m) {
    case 12u:
        return setLumQ(setSatQ(s, satQ(b)), lumQ(b));
    case 13u:
        return setLumQ(setSatQ(b, satQ(s)), lumQ(b));
    case 14u:
        return setLumQ(s, lumQ(b));
    }
    return setLumQ(b, lumQ(s));
}

// straightIn is render's: c's straight channels scaled to unit.
ivec3 straightIn(uvec4 c, int unit) {
    int a = int(c.x);
    if (a == 0) {
        return ivec3(0);
    }
    return min(ivec3(unit), (ivec3(c.yzw) * unit + a / 2) / a);
}

// blendPixel is render's: src over dst under mode m, the spec's
// premultiplied composite in integers.
uvec4 blendPixel(Prim p, uint m, uvec4 src, uvec4 dst) {
    int sa = int(src.x), da = int(dst.x);
    if (sa == 0) {
        return dst;
    }
    ivec3 sp = ivec3(src.yzw), dp = ivec3(dst.yzw);
    ivec3 bl;
    if (m < 12u) {
        ivec3 s = straightIn(src, 255), b = straightIn(dst, 255);
        for (int i = 0; i < 3; i++) {
            bl[i] = (blendSeparable(p, m, s[i], b[i], sp[i], sa, dp[i], da) + 127) / 255;
        }
    } else {
        ivec3 q = blendNonSeparable(m, straightIn(src, blendQ), straightIn(dst, blendQ));
        bl = clamp((q + 8) / 16, ivec3(0), ivec3(255));
    }
    int ao = (sa * 255 + da * (255 - sa) + 127) / 255;
    ivec3 x = sp * (255 - da) + dp * (255 - sa) + (sa * da * bl + 127) / 255;
    return uvec4(uint(ao), uvec3(min((x + 127) / 255, ivec3(ao))));
}

// atanf and atan2f are render's: Cephes' float atan, step for step,
// so conic angles agree with the CPU's bit for bit.
float atanf(float v) {
    precise float x = abs(v);
    float sign = v < 0.0 ? -1.0 : 1.0;
    precise float y0 = 0.0;
    if (x > 2.414213562373095) {
        y0 = 1.5707963267948966;
        x = -1.0 / x;
    } else if (x > 0.4142135623730950) {
        y0 = 0.7853981633974483;
        x = (x - 1.0) / (x + 1.0);
    }
    precise float z = x * x;
    precise float p = 8.05374449538e-2 * z - 1.38776856032e-1;
    p = p * z + 1.99777106478e-1;
    p = p * z - 3.33329491539e-1;
    precise float r = p * z * x + x;
    precise float out_ = sign * (y0 + r);
    return out_;
}

float atan2f(float y, float x) {
    if (x == 0.0) {
        return y > 0.0 ? 1.5707963267948966 : (y < 0.0 ? -1.5707963267948966 : 0.0);
    }
    precise float a = atanf(y / x);
    if (x > 0.0) {
        return a;
    }
    precise float b = y >= 0.0 ? a + 3.141592653589793 : a - 3.141592653589793;
    return b;
}

// gradientAt is render.GradientMap.At, in its float32 operation order;
// precise keeps each product rounded on its own, as Go's is.
float gradientAt(Prim p, vec2 px) {
    vec2 c = p.m0.xy;
    uint kind = uint(p.m2.x);
    if (kind == 1u) {
        precise float dx = (px.x - c.x) * p.m1.y;
        precise float dy = (px.y - c.y) * p.m1.z;
        precise float r = sqrt(dx * dx + dy * dy);
        return r;
    }
    if (kind == 2u) {
        precise float a = atan2f(px.x - c.x, -(px.y - c.y)) * 57.29577951308232;
        const float inv360 = 1.0 / 360.0;
        precise float t = modFloor(a - p.m1.w, 360.0, inv360) * inv360;
        return t;
    }
    precise float d = (px.x - c.x) * p.m0.z + (px.y - c.y) * p.m0.w;
    precise float t = d * p.m1.x + 0.5;
    return t;
}

// A gradient's words: the stop range's reciprocal, then each stop's
// position, color and its segment's reciprocal.
float stopPos(uint first, uint i) {
    return uintBitsToFloat(words[first + 1u + 3u * i]);
}

uvec4 stopColor(uint first, uint i) {
    return unpack(words[first + 2u + 3u * i]);
}

float stopInv(uint first, uint i) {
    return uintBitsToFloat(words[first + 3u + 3u * i]);
}

// sampleStops is render's: the extend mode folds t into the stop
// range, then the color between the stops around it, each channel
// lerped and rounded half up, all in render's float32 order.
uvec4 sampleStops(Prim p, float t0) {
    uint first = uint(p.m2.z);
    uint n = uint(p.m2.w);
    uint ext = uint(p.m2.y);
    float lo = stopPos(first, 0u);
    float hi = stopPos(first, n - 1u);
    precise float span = hi - lo;
    precise float t = t0;
    float inv = uintBitsToFloat(words[first]);
    if (span > 0.0) {
        if (ext == 1u) {
            t = lo + modFloor(t - lo, span, inv);
        } else if (ext == 2u) {
            precise float u = modFloor(t - lo, 2.0 * span, inv * 0.5);
            if (u > span) {
                u = 2.0 * span - u;
            }
            t = lo + u;
        }
    }
    if (t <= lo) {
        return stopColor(first, 0u);
    }
    for (uint i = 1u; i < n; i++) {
        float b = stopPos(first, i);
        if (t > b) {
            continue;
        }
        float a = stopPos(first, i - 1u);
        if (b - a <= 0.0) {
            return stopColor(first, i);
        }
        precise float f = (t - a) * stopInv(first, i);
        vec4 ca = vec4(stopColor(first, i - 1u));
        vec4 cb = vec4(stopColor(first, i));
        precise vec4 v = ca + (cb - ca) * f;
        return uvec4(floor(v + 0.5));
    }
    return stopColor(first, n - 1u);
}

// dot3 is one 16.16 fixed-point channel: the weighted sum rounded,
// render's applyLinearRegion.
int dot3(ivec3 k, ivec3 c) {
    return (k.x * c.x + k.y * c.y + k.z * c.z + 32768) >> 16;
}

// boxQ is render's boxDiv.q: (v + half) * recip >> 32, the high word
// of the 32x32-bit product.
uint boxQ(uint v, uint half_, uint recip) {
    uint msb, lsb;
    umulExtended(v + half_, recip, msb, lsb);
    return msb;
}

uvec4 boxQ4(uvec4 v, uint half_, uint recip) {
    return uvec4(boxQ(v.x, half_, recip), boxQ(v.y, half_, recip), boxQ(v.z, half_, recip), boxQ(v.w, half_, recip));
}

// boxRecip is render.newBoxDiv's reciprocal, 2^32/n + 1, for n from 2.
uint boxRecip(uint n) {
    uint q = 0xFFFFFFFFu / n;
    if (0xFFFFFFFFu % n == n - 1u) {
        q += 1u;
    }
    return q + 1u;
}

// upSource is render's: a destination index's lower source index at
// scale d and the next one's weight, in 1/256.
ivec2 upSource(int i, int d) {
    int f = ((2 * i + 1) * 128 - d * 128) / d;
    int i0 = f >> 8;
    return ivec2(i0, f - i0 * 256);
}

uvec4 lerp256(uvec4 a, uvec4 b, uint t) {
    return (a * (256u - t) + b * t + 128u) >> 8;
}

// refractAt is render.RefractPlan.At: how far the pixel at px samples
// off itself in 1/256 pixels, and whether it bends.
bool refractAt(Prim p, vec2 px, out ivec2 off) {
    precise float d = sdBox(px, p.box, p.radii);
    precise float k = 1.0 + d * p.m0.x;
    if (d > 0.0 || k <= 0.0) {
        return false;
    }
    precise float nx = sdBox(px + vec2(0.5, 0.0), p.box, p.radii) - sdBox(px - vec2(0.5, 0.0), p.box, p.radii);
    precise float ny = sdBox(px + vec2(0.0, 0.5), p.box, p.radii) - sdBox(px - vec2(0.0, 0.5), p.box, p.radii);
    precise float norm = sqrt(nx * nx + ny * ny);
    if (norm == 0.0) {
        return false;
    }
    precise float num = p.m0.y * k * k;
    precise float den = floor(norm * 256.0 + 0.5);
    precise float ax = floor(nx * num * 65536.0 + 0.5);
    precise float ay = floor(ny * num * 65536.0 + 0.5);
    off = ivec2(roundDiv(int(ax), int(den)), roundDiv(int(ay), int(den)));
    return true;
}

// regionTexel is the texel at i inside the region p.inner names,
// transparent past it.
uvec4 regionTexel(Prim p, ivec2 i) {
    if (any(lessThan(i, ivec2(0))) || any(greaterThanEqual(i, ivec2(p.inner.zw)))) {
        return uvec4(0u);
    }
    return texel(ivec2(p.inner.xy) + i);
}

// bilinear is render.Layer.sample: texels outside src are transparent.
uvec4 bilinear(vec4 src, vec2 at) {
    vec2 f0 = floor(at);
    precise vec2 f = at - f0;
    ivec2 i = ivec2(f0);
    ivec4 s = ivec4(src);
    if (i.x < s.x - 1 || i.y < s.y - 1 || i.x >= s.z || i.y >= s.w) {
        return uvec4(0u);
    }
    vec4 p[4];
    for (int k = 0; k < 4; k++) {
        ivec2 q = i + ivec2(k & 1, k >> 1);
        p[k] = (q.x < s.x || q.y < s.y || q.x >= s.z || q.y >= s.w) ? vec4(0.0) : vec4(texel(q));
    }
    precise vec2 g = 1.0 - f;
    precise vec4 top = p[0] * g.x + p[1] * f.x;
    precise vec4 bottom = p[2] * g.x + p[3] * f.x;
    precise vec4 v = top * g.y + bottom * f.y;
    return uvec4(clamp(floor(v + 0.5), 0.0, 255.0));
}

// mappedRow is render's row: (r.x*q.x + r.y*q.y) + r.z, each product
// rounded on its own.
float mappedRow(vec3 r, vec2 q) {
    precise float v = r.x * q.x + r.y * q.y + r.z;
    return v;
}

void main() {
    Prim p = prims[vPrim];
    vec2 px = gl_FragCoord.xy;
    uint kind = p.info.x;
    uint alpha = p.info.y;
    float bright = uintBitsToFloat(p.info.z);
    uint flags = p.info.w;
    uvec4 c;
    switch (kind) {
    case kindRect:
        c = unpack(p.cols.x);
        break;
    case kindRRect:
        c = covered(unpack(p.cols.x), coverage(sdBox(px, p.box, p.radii)));
        break;
    case kindRounded: {
        float cov = 0.5 - sdRoundRect(px, p.box, p.radii.x);
        if (cov < 0.0) {
            discard;
        }
        c = coverageScale(unpack(p.cols.x), cov255(min(cov, 1.0)));
        break;
    }
    case kindBorder: {
        float cov = coverage(sdBox(px, p.box, p.radii));
        if (cov > 0.0 && p.inner.z > p.inner.x && p.inner.w > p.inner.y) {
            cov = min(cov, 1.0 - coverage(sdBox(px, p.inner, p.iradii)));
        }
        if (cov <= 0.0) {
            discard;
        }
        uint side = borderSide(px, p.box, p.widths);
        c = covered(unpack(p.cols[side]), cov);
        break;
    }
    case kindTexture:
        c = texel(ivec2(px - p.inner.xy));
        if (c == uvec4(0u)) {
            discard;
        }
        if ((flags & flagOpaqueStores) != 0u && c.x == 255u && alpha == 255u) {
            flags |= flagRaw;
        }
        break;
    case kindMask: {
        uint m = uint(floor(texelFetch(tex, ivec2(px - p.inner.xy), 0).r * 255.0 + 0.5));
        if (m == 0u) {
            discard;
        }
        c = coverageScale(unpack(p.cols.x), m);
        break;
    }
    case kindStretch: {
        ivec2 b = ivec2(floor(px)) - ivec2(p.box.xy);
        ivec2 at = ivec2(stretch(b.x, int(p.box.z), int(p.radii.x)), stretch(b.y, int(p.box.w), int(p.radii.y)));
        uint m = uint(floor(texelFetch(tex, at + ivec2(p.inner.xy), 0).r * 255.0 + 0.5));
        if (m == 0u) {
            discard;
        }
        c = coverageScale(unpack(p.cols.x), m);
        break;
    }
    case kindImage: {
        ivec2 at = (ivec2(floor(px)) - ivec2(p.box.xy)) * ivec2(p.radii.xy) / ivec2(p.box.zw);
        c = texel(at);
        if (c == uvec4(0u)) {
            discard;
        }
        break;
    }
    case kindGradient:
        c = covered(sampleStops(p, gradientAt(p, px)), coverage(sdBox(px, p.box, p.radii)));
        break;
    case kindLine: {
        // render.LineGeometry.Coverage, in its order.
        precise vec2 q = px - p.box.xy;
        precise float t = clamp((q.x * p.box.z + q.y * p.box.w) * p.radii.x, 0.0, 1.0);
        precise vec2 e = q - p.box.zw * t;
        precise float d = sqrt(e.x * e.x + e.y * e.y);
        precise float cov = clamp(p.radii.y + 0.5 - d, 0.0, 1.0);
        if (cov == 0.0) {
            discard;
        }
        c = coverageScale(unpack(p.cols.x), cov255(cov));
        break;
    }
    case kindArc: {
        // render.ArcGeometry.Coverage, in its order.
        const float turn = 6.283185307179586;
        precise float dx = px.x - p.box.x;
        precise float dy = px.y - p.box.y;
        precise float d;
        if (p.radii.z != 0.0 || modFloor(atan2f(dy, dx) - p.radii.x, turn, 1.0 / turn) <= p.radii.y) {
            d = abs(sqrt(dx * dx + dy * dy) - p.box.z) - p.box.w;
        } else {
            precise vec2 a = px - p.inner.xy;
            precise vec2 b = px - p.inner.zw;
            d = min(sqrt(a.x * a.x + a.y * a.y), sqrt(b.x * b.x + b.y * b.y)) - p.box.w;
        }
        c = covered(unpack(p.cols.x), coverage(d));
        break;
    }
    case kindCover: {
        // render.CoverGeometry.At, in its order.
        precise float cov = min(1.0, 0.5 - sdRoundRect(px, p.box, p.radii.x));
        if (cov <= 0.0) {
            discard;
        }
        ivec2 b = ivec2(floor(px)) - ivec2(p.box.xy);
        precise float fx = (float(b.x) + p.radii.y) * p.radii.w;
        precise float fy = (float(b.y) + p.radii.z) * p.radii.w;
        ivec2 at = clamp(ivec2(int(fx), int(fy)), ivec2(0), ivec2(p.inner.xy) - 1);
        c = coverageScale(texel(at), cov255(cov));
        if (c == uvec4(0u)) {
            discard;
        }
        break;
    }
    case kindMatrix: {
        // render's applyMatrixRegion: the linear step in 16.16 fixed
        // point on the premultiplied pixel, any other in float32 on the
        // straight color, re-premultiplied.
        c = texel(ivec2(floor(px)));
        if (c == uvec4(0u)) {
            break;
        }
        ivec4 q = ivec4(c);
        if (p.m0.w != 0.0) {
            ivec3 rgb = q.yzw;
            ivec3 v = ivec3(dot3(ivec3(p.m0.xyz), rgb), dot3(ivec3(p.m1.xyz), rgb), dot3(ivec3(p.m2.xyz), rgb));
            c = uvec4(c.x, uvec3(clamp(v, ivec3(0), ivec3(q.x))));
            break;
        }
        uint a = c.x;
        uvec4 st = uvec4((c.yzw * 255u + a / 2u) / a, a);
        float m[20] = float[20](p.box.x, p.box.y, p.box.z, p.box.w, p.radii.x, p.radii.y, p.radii.z, p.radii.w,
            p.inner.x, p.inner.y, p.inner.z, p.inner.w, p.iradii.x, p.iradii.y, p.iradii.z, p.iradii.w,
            p.widths.x, p.widths.y, p.widths.z, p.widths.w);
        uvec4 o;
        for (int r = 0; r < 4; r++) {
            precise float v = m[r * 5 + 4];
            for (int k = 0; k < 4; k++) {
                precise float prod = m[r * 5 + k] * float(st[k]);
                v = v + prod;
            }
            o[r] = uint(clamp(floor(v + 0.5), 0.0, 255.0));
        }
        precise float pm = float(o.w) * p.m1.w;
        uvec3 rgb;
        for (int i = 0; i < 3; i++) {
            precise float x = float(o[i]) * pm;
            rgb[i] = uint(clamp(floor(x + 0.5), 0.0, 255.0));
        }
        c = uvec4(o.w, rgb);
        break;
    }
    case kindCrossFade: {
        // render.Layer.CrossFade: each channel (a*(255-w) + b*w + 127)/255.
        ivec2 at = ivec2(floor(px));
        uvec4 a = texel(at);
        uvec4 b = texel2(at);
        uint w = p.cols.x;
        c = a == b ? a : (a * (255u - w) + b * w + 127u) / 255u;
        break;
    }
    case kindMaskLayer: {
        // render.Layer.ApplyMaskLayer: the mask's alpha, or its
        // luminance (integer weights over the straight channels).
        ivec2 at = ivec2(floor(px));
        uvec4 m = texel2(at);
        uint cov = m.x;
        if (p.cols.x != 0u) {
            uvec3 st = m.x == 0u ? uvec3(0u) : (m.yzw * 255u + m.x / 2u) / m.x;
            cov = (st.x * 77u + st.y * 151u + st.z * 28u) / 256u * m.x / 255u;
        }
        c = masked(texel(at), cov);
        break;
    }
    case kindPathMask: {
        // render.Layer.ApplyPathMask: the path's coverage inside its
        // box (from the mask uploaded at radii.xy), none outside.
        ivec2 at = ivec2(floor(px));
        ivec4 b = ivec4(p.box);
        uint cov = 0u;
        if (at.x >= b.x && at.y >= b.y && at.x < b.z && at.y < b.w) {
            cov = uint(floor(texelFetch(tex2, at - b.xy + ivec2(p.radii.xy), 0).r * 255.0 + 0.5));
        }
        c = masked(texel(at), cov);
        break;
    }
    case kindBoxH:
    case kindBoxV: {
        // render's boxPassH and boxPassV: the 2r+1 taps inside the
        // working rect summed, rounded and divided.
        ivec2 at = ivec2(floor(px));
        ivec4 b = ivec4(p.box);
        int r = int(p.radii.x);
        ivec2 axis = kind == kindBoxH ? ivec2(1, 0) : ivec2(0, 1);
        uvec4 sum = uvec4(0u);
        for (int k = -r; k <= r; k++) {
            ivec2 q = at + axis * k;
            if (q.x >= b.x && q.y >= b.y && q.x < b.z && q.y < b.w) {
                sum += texel(q);
            }
        }
        c = boxQ4(sum, p.cols.x, p.cols.y);
        break;
    }
    case kindDownsample: {
        // render's downsample: the d x d block's average, a block cut by
        // the region's edge averaging what it holds.
        ivec2 at = ivec2(floor(px)) - ivec2(p.box.xy);
        int d = int(p.radii.x);
        ivec2 lo = ivec2(p.box.xy) + at * d;
        ivec2 hi = min(lo + d, ivec2(p.box.zw));
        uvec4 sum = uvec4(0u);
        for (int y = lo.y; y < hi.y; y++) {
            for (int x = lo.x; x < hi.x; x++) {
                sum += texel(ivec2(x, y));
            }
        }
        uint n = uint((hi.x - lo.x) * (hi.y - lo.y));
        c = n == 1u ? sum : boxQ4(sum, n / 2u, boxRecip(n));
        break;
    }
    case kindUpsample: {
        // render's upsample: each row pair blended at the row weight,
        // then across at the column weight, all in 1/256.
        ivec2 at = ivec2(floor(px)) - ivec2(p.box.xy);
        int d = int(p.radii.x);
        ivec2 small = ivec2(p.box.zw) - ivec2(p.box.xy);
        ivec2 sy = upSource(at.y, d);
        ivec2 sx = upSource(at.x, d);
        uint tx = sx.x < 0 ? 0u : uint(sx.y);
        int x0 = clamp(sx.x, 0, small.x - 1);
        int x1 = min(small.x - 1, x0 + 1);
        int ya = clamp(sy.x, 0, small.y - 1);
        int yb = clamp(sy.x + 1, 0, small.y - 1);
        ivec2 o = ivec2(p.box.xy);
        uvec4 c0 = lerp256(texel(o + ivec2(x0, ya)), texel(o + ivec2(x0, yb)), uint(sy.y));
        uvec4 c1 = lerp256(texel(o + ivec2(x1, ya)), texel(o + ivec2(x1, yb)), uint(sy.y));
        c = lerp256(c0, c1, tx);
        break;
    }
    case kindGrain: {
        // render.Canvas.PaintGrain: the noise around its midpoint
        // lightens or darkens, by the strength and the box's coverage.
        uint n = grainNoise(ivec2(floor(px)) - ivec2(p.cols.yz));
        uint amt = n < 128u ? 127u - n : n - 128u;
        uint a = amt * 2u * p.cols.x / 255u * cov255(coverage(sdBox(px, p.box, p.radii))) / 255u;
        if (a == 0u) {
            discard;
        }
        c = n < 128u ? uvec4(a, 0u, 0u, 0u) : uvec4(a);
        break;
    }
    case kindImageTiles: {
        // render.Canvas.PaintImageTiles: the tile's texel, nearest
        // sampled through the image's texel scale, under the clip.
        vec2 uv;
        if (!tileAt(p, px, uv)) {
            discard;
        }
        float cov = coverage(sdBox(px, p.box, p.radii));
        if (cov <= 0.0) {
            discard;
        }
        precise float fx = uv.x * p.widths.z;
        precise float fy = uv.y * p.widths.w;
        ivec2 at = clamp(ivec2(int(fx), int(fy)), ivec2(0), ivec2(p.cols.xy) - 1);
        c = covered(texel(at), cov);
        break;
    }
    case kindGradientTiles: {
        // render.Canvas.PaintGradientTiles: each tile its own gradient
        // box, sampled at the offset into it.
        vec2 uv;
        if (!tileAt(p, px, uv)) {
            discard;
        }
        float cov = coverage(sdBox(px, p.box, p.radii));
        if (cov <= 0.0) {
            discard;
        }
        c = covered(sampleStops(p, gradientAt(p, uv)), cov);
        break;
    }
    case kindBlend: {
        // render.Canvas.DrawLayer under a blend mode: the layer's pixel
        // at its opacity over the backdrop copy's, through blendPixel.
        ivec2 at = ivec2(floor(px));
        uvec4 dst = texel(at);
        uvec4 src = texel2(at);
        c = dst;
        if (src == uvec4(0u)) {
            break;
        }
        if (p.cols.z != 255u) {
            src = (src * p.cols.z + 127u) / 255u;
        }
        c = blendPixel(p, p.cols.x, src, dst);
        break;
    }
    case kindRefract: {
        // render's refractRegion: a bending pixel samples the region's
        // copy bilinearly at its displacement; the rest keep theirs.
        ivec2 at = ivec2(floor(px));
        c = texel(at);
        ivec2 off;
        if (!refractAt(p, px, off)) {
            break;
        }
        ivec2 pos = (at - ivec2(p.inner.xy)) * 256 + off;
        ivec2 i0 = pos >> 8;
        uvec2 t = uvec2(pos - i0 * 256);
        uvec4 top = lerp256(regionTexel(p, i0), regionTexel(p, i0 + ivec2(1, 0)), t.x);
        uvec4 bot = lerp256(regionTexel(p, i0 + ivec2(0, 1)), regionTexel(p, i0 + ivec2(1, 1)), t.x);
        c = lerp256(top, bot, t.y);
        break;
    }
    case kindSilhouette: {
        // render's dropShadowRegion: the region's alpha laid into the
        // buffer at the margin, nothing around it.
        ivec2 at = ivec2(floor(px)) - int(p.radii.x) + ivec2(p.box.xy);
        ivec4 b = ivec4(p.box);
        c = uvec4(0u);
        if (at.x >= b.x && at.y >= b.y && at.x < b.z && at.y < b.w) {
            c.x = texel(at).x;
        }
        break;
    }
    case kindShadow: {
        // render's dropShadowRegion: the tint scaled by the blurred
        // silhouette's alpha at the offset, under the pixel.
        ivec2 at = ivec2(floor(px));
        ivec2 q = at - ivec2(p.box.xy) + ivec2(p.radii.xy);
        c = texel(at);
        if (q.x >= 0 && q.y >= 0 && q.x < int(p.radii.z) && q.y < int(p.radii.w)) {
            uint a = texel2(q).x;
            uvec4 sh = unpack(p.cols.x) * a / 255u;
            c += (sh * (255u - c.x) + 127u) / 255u;
        }
        break;
    }
    case kindComposite: {
        // render.Mapped.At, in its order.
        precise float w = mappedRow(p.m2.xyz, px);
        precise float sx = mappedRow(p.m0.xyz, px) / w;
        precise float sy = mappedRow(p.m1.xyz, px) / w;
        precise float fw = mappedRow(p.widths.xyz, vec2(sx, sy));
        if (!(fw > 1e-9)) {
            discard;
        }
        c = bilinear(p.inner, vec2(sx, sy) - 0.5);
        if (c == uvec4(0u)) {
            discard;
        }
        break;
    }
    default:
        discard;
    }
    if ((flags & flagRaw) == 0u) {
        c = shade(c, alpha, bright, (flags & flagNoBright) != 0u);
    }
    outColor = vec4(c.y, c.z, c.w, c.x) / 255.0;
}
