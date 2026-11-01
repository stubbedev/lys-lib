#version 450
#extension GL_GOOGLE_include_directive : require
#include "comp.glsl"

layout(set = 0, binding = 0) uniform sampler2D tex;

layout(constant_id = 0) const float kr = 0.299;
layout(constant_id = 1) const float kb = 0.114;
layout(constant_id = 2) const float lumaScale = 1.0;
layout(constant_id = 3) const float lumaBase = 0.0;
layout(constant_id = 4) const float chromaScale = 1.0;
layout(constant_id = 5) const float chromaBase = 0.5;

layout(location = 0) in vec2 vUV;
layout(location = 0) out vec4 color;

void main() {
    vec4 s = texture(tex, vUV);
    float y = s.r * lumaScale - lumaBase;
    float cb = s.g * chromaScale - chromaBase;
    float cr = s.b * chromaScale - chromaBase;
    float r = y + 2.0 * (1.0 - kr) * cr;
    float b = y + 2.0 * (1.0 - kb) * cb;
    float g = (y - kr * r - kb * b) / (1.0 - kr - kb);
    color = vec4(clamp(vec3(r, g, b), 0.0, 1.0), s.a) * (draw.target.z * maskCoverage(gl_FragCoord.xy, draw.keep, draw.cut, draw.radii, draw.merge, draw.target.w));
}
