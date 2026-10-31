float roundRectCoverage(vec2 p, vec4 r, float radius) {
    vec2 half_ = (r.zw - r.xy) * 0.5;
    float rad = min(radius, min(half_.x, half_.y));
    vec2 q = abs(p - (r.xy + r.zw) * 0.5) - half_ + vec2(rad);
    float d = length(max(q, vec2(0.0))) + min(max(q.x, q.y), 0.0) - rad;
    return clamp(0.5 - d, 0.0, 1.0);
}

float maskCoverage(vec2 p, vec4 keep, vec4 cut, vec2 radii) {
    float k = keep.z > keep.x && keep.w > keep.y ? roundRectCoverage(p, keep, radii.x) : 1.0;
    float c = cut.z > cut.x && cut.w > cut.y ? roundRectCoverage(p, cut, radii.y) : 0.0;
    return k * (1.0 - c);
}
