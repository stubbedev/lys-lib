float colorCurve(float x, vec4 p, vec4 q) {
    float m = abs(x);
    float y = m >= q.x ? pow(p.y * m + p.z, p.x) + q.y : p.w * m + q.z;
    return x < 0.0 ? -y : y;
}

float colorPQDecode(float v) {
    float m = pow(max(v, 0.0), 1.0 / 78.84375);
    return pow(max(m - 0.8359375, 0.0) / (18.8515625 - 18.6875 * m), 1.0 / 0.1593017578125);
}

float colorPQEncode(float l) {
    float m = pow(max(l, 0.0), 0.1593017578125);
    return pow((0.8359375 + 18.8515625 * m) / (1.0 + 18.6875 * m), 78.84375);
}

float colorHLGDecode(float v) {
    float e = max(v, 0.0);
    return e <= 0.5 ? e * e / 3.0 : (exp((e - 0.55991073) / 0.17883277) + 0.28466892) / 12.0;
}

vec3 colorDecode(vec3 c) {
    float kind = CP[1].w;
    if (kind < 0.5) {
        return c;
    }
    if (kind < 1.5) {
        return vec3(colorCurve(c.r, CP[0], CP[1]), colorCurve(c.g, CP[0], CP[1]), colorCurve(c.b, CP[0], CP[1]));
    }
    if (kind < 2.5) {
        return vec3(colorPQDecode(c.r), colorPQDecode(c.g), colorPQDecode(c.b));
    }
    vec3 scene = vec3(colorHLGDecode(c.r), colorHLGDecode(c.g), colorHLGDecode(c.b));
    return scene * pow(max(dot(CP[2].xyz, scene), 0.000001), CP[2].w);
}

vec3 colorEncode(vec3 c) {
    float kind = CP[8].w;
    if (kind < 0.5) {
        return c;
    }
    if (kind < 1.5) {
        return vec3(colorCurve(c.r, CP[7], CP[8]), colorCurve(c.g, CP[7], CP[8]), colorCurve(c.b, CP[7], CP[8]));
    }
    return vec3(colorPQEncode(c.r), colorPQEncode(c.g), colorPQEncode(c.b));
}

float colorFinite(float v) {
    return v > -1000000.0 && v < 1000000.0 ? v : 0.0;
}

vec4 colorApply(vec4 c) {
    if (!(c.a > 0.0)) {
        return vec4(0.0);
    }
    float a = min(c.a, 1.0);
    vec3 rgb = colorDecode(c.rgb / c.a);
    rgb = vec3(dot(CP[3].xyz, rgb), dot(CP[4].xyz, rgb), dot(CP[5].xyz, rgb));
    float peak = max(rgb.r, max(rgb.g, rgb.b));
    if (CP[6].y > 0.0 && peak > CP[6].x) {
        float over = peak - CP[6].x;
        rgb *= (CP[6].x + over / (1.0 + over * CP[6].y)) / peak;
    }
    rgb = colorEncode(rgb * CP[6].w);
    return vec4(colorFinite(rgb.r), colorFinite(rgb.g), colorFinite(rgb.b), 1.0) * a;
}
