float cutCoverage(CUT_SAMPLER t, vec2 p, vec4 axes, vec4 base) {
    vec2 uv = base.xy + p.x * axes.xy + p.y * axes.zw;
    if (uv.x < 0.0 || uv.y < 0.0 || uv.x > 1.0 || uv.y > 1.0) {
        return 0.0;
    }
    return max(SAMPLE(t, uv).a, base.w) > base.z ? 1.0 : 0.0;
}
