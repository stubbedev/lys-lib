float roundRectDistance(vec2 p, vec4 r, float radius) {
    vec2 half_ = (r.zw - r.xy) * 0.5;
    float rad = min(radius, min(half_.x, half_.y));
    vec2 q = abs(p - (r.xy + r.zw) * 0.5) - half_ + vec2(rad);
    return length(max(q, vec2(0.0))) + min(max(q.x, q.y), 0.0) - rad;
}

float edgeCoverage(float d) {
    return clamp(0.5 - d, 0.0, 1.0);
}

float falloffCoverage(float d, vec2 falloff) {
    if (falloff.x <= 0.0) {
        return edgeCoverage(d);
    }
    return pow(clamp(1.0 - d / falloff.x, 0.0, 1.0), falloff.y);
}

float smoothMin(float a, float b, float k) {
    if (k <= 0.0) {
        return min(a, b);
    }
    float h = max(k - abs(a - b), 0.0) / k;
    return min(a, b) - h * h * k * 0.25;
}

float gooCoverage(vec2 p, vec4 a, vec4 b, vec3 shape) {
    return edgeCoverage(smoothMin(roundRectDistance(p, a, shape.x), roundRectDistance(p, b, shape.y), shape.z));
}

float maskCoverage(vec2 p, vec4 keep, vec4 cut, vec4 radii, vec4 merge, float smoothing) {
    float k = 1.0;
    if (keep.z > keep.x && keep.w > keep.y) {
        float d = roundRectDistance(p, keep, radii.x);
        if (merge.z > merge.x && merge.w > merge.y) {
            d = smoothMin(d, roundRectDistance(p, merge, radii.x), smoothing);
        }
        k = falloffCoverage(d, radii.zw);
    }
    if (cut.z > cut.x && cut.w > cut.y) {
        k *= 1.0 - edgeCoverage(roundRectDistance(p, cut, radii.y));
    }
    return k;
}

vec2 gradientUV(vec2 p, vec4 line, float stops) {
    vec2 d = line.zw - line.xy;
    float t = clamp(dot(p - line.xy, d) / dot(d, d), 0.0, 1.0);
    return vec2((t * (stops - 1.0) + 0.5) / stops, 0.5);
}

#ifdef SAMPLE
vec4 kawaseDown(sampler2D tex, vec2 uv, vec2 h) {
    vec4 s = SAMPLE(tex, uv) * 4.0;
    s += SAMPLE(tex, uv - h);
    s += SAMPLE(tex, uv + h);
    s += SAMPLE(tex, uv + vec2(h.x, -h.y));
    s += SAMPLE(tex, uv - vec2(h.x, -h.y));
    return s / 8.0;
}

vec4 kawaseUp(sampler2D tex, vec2 uv, vec2 h) {
    vec4 s = SAMPLE(tex, uv + vec2(-h.x * 2.0, 0.0));
    s += SAMPLE(tex, uv + vec2(-h.x, h.y)) * 2.0;
    s += SAMPLE(tex, uv + vec2(0.0, h.y * 2.0));
    s += SAMPLE(tex, uv + vec2(h.x, h.y)) * 2.0;
    s += SAMPLE(tex, uv + vec2(h.x * 2.0, 0.0));
    s += SAMPLE(tex, uv + vec2(h.x, -h.y)) * 2.0;
    s += SAMPLE(tex, uv + vec2(0.0, -h.y * 2.0));
    s += SAMPLE(tex, uv + vec2(-h.x, -h.y)) * 2.0;
    return s / 12.0;
}
#endif
