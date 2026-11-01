layout(constant_id = 0) const float kr = 0.299;
layout(constant_id = 1) const float kb = 0.114;
layout(constant_id = 2) const float lumaScale = 1.0;
layout(constant_id = 3) const float lumaBase = 0.0;
layout(constant_id = 4) const float chromaScale = 1.0;
layout(constant_id = 5) const float chromaBase = 0.5;

vec3 ycbcrToRGB(float luma, float blue, float red) {
    float y = luma * lumaScale - lumaBase;
    float cb = blue * chromaScale - chromaBase;
    float cr = red * chromaScale - chromaBase;
    float r = y + 2.0 * (1.0 - kr) * cr;
    float b = y + 2.0 * (1.0 - kb) * cb;
    float g = (y - kr * r - kb * b) / (1.0 - kr - kb);
    return clamp(vec3(r, g, b), 0.0, 1.0);
}
