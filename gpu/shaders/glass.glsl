// glassBackdrop is the backdrop at px, bent through the bezel inside box
// as render.RefractPlan.At bends it: along the edge's normal, by amount
// at the edge falling off quadratically to none invBezel's reciprocal
// inside it. perPixel is one target pixel in texture coordinates.
vec4 glassBackdrop(sampler2D tex, vec2 uv, vec2 px, vec2 perPixel, vec4 box, float radius, float invBezel, float amount) {
    if (amount > 0.0) {
        float d = roundRectDistance(px, box, radius);
        float k = 1.0 + d * invBezel;
        if (d <= 0.0 && k > 0.0) {
            vec2 n = vec2(
                roundRectDistance(px + vec2(0.5, 0.0), box, radius) - roundRectDistance(px - vec2(0.5, 0.0), box, radius),
                roundRectDistance(px + vec2(0.0, 0.5), box, radius) - roundRectDistance(px - vec2(0.0, 0.5), box, radius));
            float norm = length(n);
            if (norm > 0.0) {
                uv += n / norm * amount * k * k * perPixel;
            }
        }
    }
    return SAMPLE(tex, uv);
}

// glassAdjust is Glass.Adjust: saturate(), brightness() and contrast()
// over a premultiplied colour, each clamped as filter-effects-1 says.
vec4 glassAdjust(vec4 c, vec3 adjust) {
    if (c.a <= 0.0) {
        return c;
    }
    vec3 rgb = c.rgb / c.a;
    float luma = dot(rgb, vec3(0.2126, 0.7152, 0.0722));
    rgb = clamp(vec3(luma) + (rgb - vec3(luma)) * adjust.x, 0.0, 1.0);
    rgb = clamp(rgb * adjust.y, 0.0, 1.0);
    rgb = clamp((rgb - vec3(0.5)) * adjust.z + vec3(0.5), 0.0, 1.0);
    return vec4(rgb * c.a, c.a);
}
