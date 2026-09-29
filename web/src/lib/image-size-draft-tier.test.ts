import assert from "node:assert/strict";
import test from "node:test";
import { normalizeImageSizeSetting, type ImageCapabilityConfig } from "./model-capabilities";

// qwen-image-2.1 在 CHANNEL_000002 的真实能力配置（capability_config_json.image）。
// 它原本还声明了 1024x1024 / 1024x1792 / 1792x1024——sd.cpp 跑不动（实测 1.05M px
// 起卡死在 step 3/20，永不返回），前端照单全收于是每张图都死锁。已从声明里删掉。
const qwenImage: ImageCapabilityConfig = {
    version: 1,
    references: { promptMaxChars: 32000, maxImages: 4, maxImageBytes: 10485760, maskSupported: false },
    size: {
        parameter: "size",
        values: ["512x512", "512x288", "768x432", "1024x576", "576x1024", "864x480", "480x864"],
        default: "864x480",
        allowCustom: false,
    },
    quality: { supported: false, values: [], default: "auto" },
    transparentBackground: { supported: false, default: false },
    responseFormat: { supported: false },
    outputFormat: { supported: false },
    maxOutputs: 1,
} as unknown as ImageCapabilityConfig;

test("比例请求解析成该比例下的草稿档，而不是掉回 size.default", () => {
    assert.equal(normalizeImageSizeSetting(qwenImage, "16:9"), "864x480");
    assert.equal(normalizeImageSizeSetting(qwenImage, "9:16"), "480x864");
});

test("草稿档永远不选到会死锁的尺寸（sd.cpp >1M px 卡死）", () => {
    for (const ratio of ["1:1", "3:2", "2:3", "4:3", "3:4", "16:9", "9:16"]) {
        const size = normalizeImageSizeSetting(qwenImage, ratio);
        const [w, h] = size.split("x").map(Number);
        assert.ok(w * h <= 450_000, `${ratio} → ${size} 超出草稿档像素上限`);
    }
});

test("声明过的像素值原样透传——能力配置里声明的都必须是 sd.cpp 跑得动的", () => {
    for (const size of qwenImage.size.values) {
        assert.equal(normalizeImageSizeSetting(qwenImage, size), size);
        const [w, h] = size.split("x").map(Number);
        assert.ok(w * h <= 700_000, `${size} 超过 sd.cpp 实测安全上限，不该出现在声明里`);
    }
});

test("localStorage 里遗留的死锁尺寸回退到 size.default（不会再原样发出去）", () => {
    for (const stale of ["1024x1024", "1024x1792", "1792x1024"]) {
        assert.equal(normalizeImageSizeSetting(qwenImage, stale), "864x480");
    }
});

test("auto 与不支援的比例回退到声明值，不是 undefined", () => {
    assert.equal(normalizeImageSizeSetting(qwenImage, "auto"), "auto");
    assert.ok(qwenImage.size.values.includes(normalizeImageSizeSetting(qwenImage, "21:9")));
});
