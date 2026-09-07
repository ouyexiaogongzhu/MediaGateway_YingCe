import { expect, test } from "bun:test";

import { storyboardVideoInput } from "../src/pages/canvas/canvas-media-generation-executors";
import { submitBackendGenerationTask } from "../src/services/api/generation-task";
import type { CreateTaskInput } from "../src/services/api/task-center";
import { defaultConfig, type AiConfig } from "../src/stores/use-config-store";
import { CanvasNodeType, type CanvasNodeData } from "../src/types/canvas";

const videoConfig = (): AiConfig => ({
    ...defaultConfig,
    model: "test::MiniMax-H3",
    videoModel: "test::MiniMax-H3",
    models: ["test::MiniMax-H3"],
    videoModels: ["test::MiniMax-H3"],
    channels: [{ id: "test", name: "测试渠道", baseUrl: "https://api.example.com", apiKey: "test-key", apiFormat: "openai", interfaceType: "newapi-channel-2", models: ["MiniMax-H3"], scope: "user" as const, modelCosts: [] }],
});

function node(id: string, type: CanvasNodeData["type"], metadata: CanvasNodeData["metadata"]): CanvasNodeData {
    return { id, type, title: id, position: { x: 0, y: 0 }, width: 100, height: 100, metadata };
}

const shotImage = (content?: string) => node("img-1", CanvasNodeType.Image, { content, workflowKind: "shot" });
const shotVideo = (metadata: CanvasNodeData["metadata"]) => node("video-1", CanvasNodeType.Video, { workflowKind: "shot", ...metadata });

test("storyboardVideoInput keeps dialogue and keyframe frame", () => {
    const input = storyboardVideoInput(shotVideo({ storyboardDialogue: "  你好呀  ", videoStartFrameNodeId: "img-1" }), [shotImage("data:image/png;base64,AAA")]);
    expect(input).toEqual({ first_frame_image: "data:image/png;base64,AAA", mute_audio: false, dialogue: "你好呀" });
});

test("whitespace-only dialogue means mute without dialogue key", () => {
    const input = storyboardVideoInput(shotVideo({ storyboardDialogue: "   " }), []);
    expect(input).toEqual({ mute_audio: true });
    expect(Object.keys(input!)).not.toContain("dialogue");
});

test("missing or empty first-frame image node omits first_frame_image instead of sending empty string", () => {
    const deleted = storyboardVideoInput(shotVideo({ storyboardDialogue: "台词", videoStartFrameNodeId: "img-gone" }), [shotImage("data:image/png;base64,AAA")]);
    expect(deleted).toEqual({ mute_audio: false, dialogue: "台词" });

    const empty = storyboardVideoInput(shotVideo({ storyboardDialogue: "", videoStartFrameNodeId: "img-1" }), [shotImage("")]);
    expect(empty).toEqual({ mute_audio: true });

    const wrongType = storyboardVideoInput(shotVideo({ videoStartFrameNodeId: "img-1" }), [node("img-1", CanvasNodeType.Video, { content: "data:image/png;base64,AAA" })]);
    expect(wrongType).toEqual({ mute_audio: true });
});

test("non-shot video node and undefined source produce no videoInput fields", () => {
    expect(storyboardVideoInput(node("video-1", CanvasNodeType.Video, { storyboardDialogue: "台词", videoStartFrameNodeId: "img-1" }), [shotImage("data:image/png;base64,AAA")])).toBeUndefined();
    expect(storyboardVideoInput(undefined, [shotImage("data:image/png;base64,AAA")])).toBeUndefined();
});

const dependencies = {
    createTask: async (input: CreateTaskInput) => input as never,
    waitTask: async () => {
        throw new Error("should not wait");
    },
    runLocal: async () => ({ mode: "video" as const }),
    createId: () => "id-1",
    now: () => "2026-08-30T00:00:00.000Z",
};

test("canvas_video input spreads storyboard protocol fields at top level", async () => {
    const created = (await submitBackendGenerationTask({
        projectId: "project-1",
        mode: "video",
        prompt: "镜头",
        config: videoConfig(),
        videoInput: { first_frame_image: "data:image/png;base64,AAA", mute_audio: false, dialogue: "台词" },
    }, dependencies)) as unknown as CreateTaskInput;
    expect(created.type).toBe("canvas_video");
    expect(created.input.first_frame_image).toBe("data:image/png;base64,AAA");
    expect(created.input.mute_audio).toBe(false);
    expect(created.input.dialogue).toBe("台词");
});

test("non-video modes and plain video tasks never carry the storyboard fields", async () => {
    const image = (await submitBackendGenerationTask({
        projectId: "project-1",
        mode: "image",
        prompt: "图",
        config: videoConfig(),
        videoInput: { mute_audio: true },
    }, dependencies)) as unknown as CreateTaskInput;
    expect(image.type).toBe("canvas_image");
    expect(image.input.mute_audio).toBeUndefined();
    expect(image.input.first_frame_image).toBeUndefined();
    expect(image.input.dialogue).toBeUndefined();

    const plain = (await submitBackendGenerationTask({
        projectId: "project-1",
        mode: "video",
        prompt: "普通视频",
        config: videoConfig(),
    }, dependencies)) as unknown as CreateTaskInput;
    expect(plain.input.mute_audio).toBeUndefined();
    expect(plain.input.first_frame_image).toBeUndefined();
    expect(plain.input.dialogue).toBeUndefined();
});
