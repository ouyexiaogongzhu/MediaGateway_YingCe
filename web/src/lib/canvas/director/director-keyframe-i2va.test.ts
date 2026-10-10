import assert from "node:assert/strict";
import test from "node:test";

import { buildDirectorKeyframeI2vaPayload, compileDirectorKeyframeI2vaPrompt } from "./director-keyframe-i2va";

// MediaGateway server/compat_h3cweb.py _validate_context_ir 的 TS 逐规则移植（image_refs=1 分支）。
// 编译产物必须让它返回 null——这比手抄断言更贴近验收契约。
const CTX_FIELDS = ["integrated_multimodal_description:", "overall_soundscape:", "non_diegetic_music:"] as const;
const CTX_I2VA_PREAMBLE = "For the target video, at 0.00 seconds into the target video, <Picture 1> (from [Shot 1]) is fully referenced.";

function validateContextIr(prompt: string): string | null {
    const problems: string[] = [];
    const positions = CTX_FIELDS.map((field) => {
        const hits = [...prompt.matchAll(new RegExp(field.replace(/[:]/g, "\\:"), "g"))].map((m) => m.index!);
        if (!hits.length) problems.push(`missing field ${field}`);
        if (hits.length > 1) problems.push(`field ${field} appears ${hits.length} times`);
        return hits[0] ?? -1;
    });
    if (positions.every((p) => p >= 0) && JSON.stringify(positions) !== JSON.stringify([...positions].sort((a, b) => a - b))) {
        problems.push("fields out of order");
    }
    if (!prompt.startsWith(CTX_I2VA_PREAMBLE + "\n\n")) problems.push("I2VA preamble not verbatim at position 0");
    const descEnd = prompt.indexOf(CTX_FIELDS[1]);
    for (const match of prompt.matchAll(/<d>\[([^\]]*)\]/g)) {
        if (match.index! >= descEnd) problems.push(`<d> on line ${prompt.slice(0, match.index).split("\n").length} outside integrated_multimodal_description`);
        if (!match[1] || !/^[\x20-\x7e]*$/.test(match[1])) problems.push(`<d>[${match[1]}] language tag must be ASCII English`);
        if (!/\(S\d+\)/.test(prompt.slice(Math.max(0, match.index! - 300), match.index))) problems.push("<d> has no (S1) speaker id in run-up");
    }
    const music = prompt.split(CTX_FIELDS[2], 2)[1]?.trim() ?? "";
    if (music && ["NO MUSIC.", "NONE", "NO"].includes(music.toUpperCase())) problems.push(`non_diegetic_music must be N/A or prose, not ${music}`);
    return problems.length ? problems.join("; ") : null;
}

const shot = {
    prompt: "雨夜書店裡，年輕女店主在櫃檯後抬起頭，輕聲說：<d>[Chinese]我們要打烊了。</d>",
    cameraMove: "push_in" as const,
    shotSize: "medium" as const,
    duration: 5,
};

test("编译产物通过 Context-IR 校验（preamble 逐字、字段有序、<d> 位置、语言标签、说话人 id）", () => {
    const prompt = compileDirectorKeyframeI2vaPrompt({ description: shot.prompt, cameraMove: shot.cameraMove, shotSize: shot.shotSize });
    assert.equal(validateContextIr(prompt), null);
    assert.ok(prompt.startsWith("For the target video, at 0.00 seconds into the target video, <Picture 1> (from [Shot 1]) is fully referenced.\n\n"));
    const [desc, soundscape, music] = CTX_FIELDS.map((field) => prompt.indexOf(field));
    assert.ok(desc >= 0 && soundscape > desc && music > soundscape);
    assert.ok(prompt.includes("The camera pushes in with small amplitude at slow speed."));
    assert.ok(prompt.includes("a medium shot"));
    assert.ok(prompt.includes("雨夜書店裡"));
    assert.ok(prompt.includes("(S1) <d>[Chinese]我們要打烊了。</d>"));
});

test("缺说话人 id 的对白自动补 (S1)，已有 (S2) 不动；字段名撞车被剥除", () => {
    const repaired = compileDirectorKeyframeI2vaPrompt({
        ...shot,
        description: "總體氛圍：她揮手說：<d>[Chinese]再見。</d>",
    });
    assert.equal(validateContextIr(repaired), null);
    assert.ok(repaired.includes("(S1) <d>[Chinese]再見。</d>"));
    assert.equal(repaired.indexOf("overall_soundscape:"), repaired.lastIndexOf("overall_soundscape:"));

    const kept = compileDirectorKeyframeI2vaPrompt({
        ...shot,
        description: "年輕女店主 (S2) 說：<d>[Chinese]今天早點休息。</d>",
    });
    assert.equal(validateContextIr(kept), null);
    assert.ok(kept.includes("(S2) 說：<d>"));
    assert.ok(!kept.includes("(S1) <d>"));
});

test("payload：关键帧 dataURL 进 first_frame_image，对白决定 mute_audio，时长/尺寸透传", () => {
    const payload = buildDirectorKeyframeI2vaPayload({ keyframePngDataUrl: "data:image/png;base64,aGVsbG8=", shot });
    assert.equal(payload.first_frame_image, "data:image/png;base64,aGVsbG8=");
    assert.equal(payload.mute_audio, false);
    assert.equal(payload.seconds, 5);
    assert.equal(validateContextIr(payload.prompt), null);

    const silent = buildDirectorKeyframeI2vaPayload({ keyframePngDataUrl: "data:image/png;base64,aGVsbG8=", shot: { ...shot, prompt: "空街緩慢推進，無對白" }, size: "864x480" });
    assert.equal(silent.mute_audio, true);
    assert.equal(silent.size, "864x480");

    assert.throws(() => buildDirectorKeyframeI2vaPayload({ keyframePngDataUrl: "https://x/a.png", shot }));
});
