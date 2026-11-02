#version 450
#extension GL_GOOGLE_include_directive : require
#include "comp.glsl"
#include "ycbcr.glsl"

layout(set = 0, binding = 0) uniform sampler2D lumaTex;
layout(set = 0, binding = 1) uniform sampler2D blueTex;
layout(set = 0, binding = 2) uniform sampler2D redTex;

layout(constant_id = 6) const float chromaShiftX = 0.0;
layout(constant_id = 7) const float chromaShiftY = 0.0;

layout(location = 0) in vec2 vUV;
layout(location = 0) out vec4 color;

void main() {
    vec2 chromaUV = vUV + vec2(chromaShiftX, chromaShiftY) / vec2(textureSize(blueTex, 0));
    vec3 rgb = ycbcrToRGB(texture(lumaTex, vUV).r, texture(blueTex, chromaUV).r, texture(redTex, chromaUV).r);
    color = COLORED(vec4(rgb, 1.0)) * (draw.target.z * maskCoverage(gl_FragCoord.xy, draw.keep, draw.cut, draw.radii, draw.merge, draw.target.w));
}
