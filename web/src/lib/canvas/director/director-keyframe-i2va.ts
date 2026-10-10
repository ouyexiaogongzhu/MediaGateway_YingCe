// P4（docs/director3d_port_plan.md:57）：导演台镜头 + 关键帧 PNG → h3 I2VA 请求载荷。
// prompt 编译是纯函数，输出必须通过 MediaGateway server/compat_h3cweb.py 的
// _validate_context_ir(prompt, image_refs=1)：I2VA 前导逐字开头 + 空行、三字段各出现
// 恰一次且有序、<d> 只在 integrated_multimodal_description、语言标签英文 ASCII、
// 每个 <d> 前至多 300 字符内有 (S1)/(S2) 说话人 id。格式权威 = h3-prompt-writing base-en.txt。
import type { DirectorCameraMove, DirectorShot, DirectorShotSize } from "@/types/director";

/** 逐字对应 compat_h3cweb.CTX_I2VA_PREAMBLE（base-en.txt §2.1），一个字符都不能差。 */
const I2VA_PREAMBLE = "For the target video, at 0.00 seconds into the target video, <Picture 1> (from [Shot 1]) is fully referenced.";

const shotSizePhrase: Record<DirectorShotSize, string> = {
    extreme_wide: "an extreme wide shot",
    wide: "a wide shot",
    full: "a full shot",
    medium: "a medium shot",
    close_up: "a close-up shot",
    extreme_close_up: "an extreme close-up shot",
};

/** 运镜 → base-en.txt §4.3 的英语运镜句（类型+幅度+速度），镜头约 5 秒，统一慢速小幅度。 */
const cameraMovePrompt: Record<DirectorCameraMove, string> = {
    static: "The camera holds a static shot as the scene plays out.",
    push_in: "The camera pushes in with small amplitude at slow speed.",
    pull_out: "The camera pulls out with small amplitude at slow speed.",
    pan_left: "The camera pans left with small amplitude at slow speed.",
    pan_right: "The camera pans right with small amplitude at slow speed.",
    tilt_up: "The camera tilts up with small amplitude at slow speed.",
    tilt_down: "The camera tilts down with small amplitude at slow speed.",
    orbit_left: "The camera arcs left around the subject at slow speed.",
    orbit_right: "The camera arcs right around the subject at slow speed.",
    handheld: "The camera shakes slightly with subtle handheld motion.",
};

/** 用户描述里若误写了 Context-IR 字段名，会撞掉「每字段恰一次」校验，先剥掉。 */
const FIELD_COLLISION = /\b(?:integrated_multimodal_description|overall_soundscape|non_diegetic_music)\s*:/g;

/**
 * 每个 <d> 前 300 字符内补 (S1)（与 _validate_context_ir 同窗），已有说话人 id 不动。
 * ponytail: 用户把 (S1) 写在很远处的极端排版可能需要多轮收敛，循环到不动点为止；
 * 真实镜头描述 0-1 句对白，通常一轮即稳。
 */
function ensureSpeakerIds(text: string): string {
    let result = text;
    while (true) {
        const next = result.replace(/<d>/g, (match, offset) => {
            const runUp = result.slice(Math.max(0, offset - 300), offset);
            return /\(S\d+\)/.test(runUp) ? match : `(S1) ${match}`;
        });
        if (next === result) return next;
        result = next;
    }
}

export type DirectorKeyframeI2vaPromptInput = {
    /** 镜头描述（shot.prompt），可含 <d>[语言]…</d> 对白；语言标签需用户自写英文。 */
    description: string;
    cameraMove: DirectorCameraMove;
    shotSize: DirectorShotSize;
    /** 环境音正文，缺省 N/A；音乐缺省 N/A（无配乐时 base-en.txt 的规定写法）。 */
    soundscape?: string;
    music?: string;
};

/** 纯函数：镜头描述 + 运镜 → 通过 _validate_context_ir(image_refs=1) 的 I2VA prompt。 */
export function compileDirectorKeyframeI2vaPrompt(input: DirectorKeyframeI2vaPromptInput): string {
    const description = ensureSpeakerIds(input.description.replace(FIELD_COLLISION, "").trim());
    return [
        I2VA_PREAMBLE,
        "",
        `integrated_multimodal_description: [Shot 1] Cinematic, ${shotSizePhrase[input.shotSize]}. The composition, subjects, and spatial layout of <Picture 1> are fully preserved. ${cameraMovePrompt[input.cameraMove]} ${description}`,
        "",
        `overall_soundscape: ${input.soundscape?.trim() || "N/A"}`,
        "",
        `non_diegetic_music: ${input.music?.trim() || "N/A"}`,
    ].join("\n");
}

/** 对齐既有分镜流的 canvas_video 协议字段（storyboardVideoInput / generation-task videoInput）。 */
export type DirectorKeyframeI2vaPayload = {
    prompt: string;
    /** 关键帧 PNG dataURL，作为 h3 首帧。 */
    first_frame_image: string;
    mute_audio: boolean;
    seconds?: number;
    size?: string;
};

export function buildDirectorKeyframeI2vaPayload(input: {
    keyframePngDataUrl: string;
    shot: Pick<DirectorShot, "prompt" | "cameraMove" | "shotSize" | "duration">;
    /** 如 "864x480"；省略时走 Gateway 默认。 */
    size?: string;
    soundscape?: string;
    music?: string;
}): DirectorKeyframeI2vaPayload {
    if (!/^data:image\//.test(input.keyframePngDataUrl)) throw new Error("关键帧必须是 image data URL");
    const prompt = compileDirectorKeyframeI2vaPrompt({
        description: input.shot.prompt,
        cameraMove: input.shot.cameraMove,
        shotSize: input.shot.shotSize,
        soundscape: input.soundscape,
        music: input.music,
    });
    return {
        prompt,
        first_frame_image: input.keyframePngDataUrl,
        // 与分镜流同判据：无对白 → mute_audio，省掉无意义的音频生成。
        mute_audio: !/<d>\[/.test(prompt),
        ...(input.shot.duration > 0 ? { seconds: input.shot.duration } : {}),
        ...(input.size ? { size: input.size } : {}),
    };
}
