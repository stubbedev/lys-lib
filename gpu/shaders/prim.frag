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

// The info.w flags, gpu/prim.go's.
const uint flagRaw = 1u;
const uint flagOpaqueStores = 2u;
const uint flagNoBright = 4u;

layout(set = 1, binding = 0) uniform sampler2D tex;

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
    return clamp(0.5 - d, 0.0, 1.0);
}

float sdBox(vec2 p, vec4 b, vec4 rc) {
    bool left = p.x < (b.x + b.z) / 2.0;
    bool top = p.y < (b.y + b.w) / 2.0;
    float ex = left ? p.x - b.x : b.z - p.x;
    float ey = top ? p.y - b.y : b.w - p.y;
    float rad = left ? (top ? rc.x : rc.w) : (top ? rc.y : rc.z);
    float qx = rad - ex;
    float qy = rad - ey;
    return length(vec2(max(qx, 0.0), max(qy, 0.0))) + min(max(qx, qy), 0.0) - rad;
}

float sdRoundRect(vec2 p, vec4 r, float radius) {
    vec2 c = (r.xy + r.zw) / 2.0;
    vec2 h = (r.zw - r.xy) / 2.0 - radius;
    vec2 q = abs(p - c) - h;
    return length(max(q, 0.0)) + min(max(q.x, q.y), 0.0) - radius;
}

uint borderSide(vec2 p, vec4 b, vec4 w) {
    float d[4] = float[4](p.y - b.y, b.z - p.x, b.w - p.y, p.x - b.x);
    float wv[4] = float[4](w.x, w.y, w.z, w.w);
    uint best = 0u;
    float bestV = 1.0 / 0.0;
    for (uint i = 0u; i < 4u; i++) {
        if (wv[i] <= 0.0) {
            continue;
        }
        float v = d[i] / wv[i];
        if (v < bestV) {
            best = i;
            bestV = v;
        }
    }
    return best;
}

// covered applies render.Canvas.blendCov's coverage step.
uvec4 covered(uvec4 c, float cov) {
    if (cov <= 0.0) {
        discard;
    }
    if (cov >= 1.0) {
        return c;
    }
    return coverageScale(c, uint(floor(cov * 255.0 + 0.5)));
}

// bilinear is render.Layer.sample: texels outside src are transparent.
uvec4 bilinear(vec4 src, vec2 at) {
    vec2 f0 = floor(at);
    vec2 f = at - f0;
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
    vec4 top = p[0] * (1.0 - f.x) + p[1] * f.x;
    vec4 bottom = p[2] * (1.0 - f.x) + p[3] * f.x;
    return uvec4(clamp(floor(top * (1.0 - f.y) + bottom * f.y + 0.5), 0.0, 255.0));
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
        c = coverageScale(unpack(p.cols.x), uint(floor(min(cov, 1.0) * 255.0 + 0.5)));
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
    case kindComposite: {
        vec3 q = vec3(px, 1.0);
        float w = dot(p.m2.xyz, q);
        vec2 s = vec2(dot(p.m0.xyz, q), dot(p.m1.xyz, q)) / w;
        if (dot(p.widths.xyz, vec3(s, 1.0)) <= 1e-9) {
            discard;
        }
        c = bilinear(p.inner, s - 0.5);
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
