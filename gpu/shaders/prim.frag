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
    uvec4 o = uvec4(floor(vec4(c) * f + 0.5));
    return uvec4(c.x, min(uvec3(c.x), o.yzw));
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
