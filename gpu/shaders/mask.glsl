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

float maskCoverage(vec2 p, vec4 keep, vec4 cut, vec4 radii) {
    float k = 1.0;
    if (keep.z > keep.x && keep.w > keep.y) {
        k = falloffCoverage(roundRectDistance(p, keep, radii.x), radii.zw);
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
