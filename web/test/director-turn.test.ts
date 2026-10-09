import { describe, expect, test } from "bun:test";

import { createEmptyScene } from "@/lib/canvas/director/director-document";
import {
    runDirectorTurn,
    type DirectorAssistantMessage,
    type DirectorMessage,
    type DirectorToolCall,
    type DirectorToolResultMessage,
} from "@/lib/canvas/director/director-turn";

const assistant = (content: string, toolCalls?: DirectorToolCall[]): DirectorAssistantMessage => ({ role: "assistant", content, toolCalls });

const baseScene = () => {
    const scene = createEmptyScene();
    scene.objectList.push({
        threeJsonId: "box1", name: "方块", objType: "box", geometry: {},
        position: { x: 0, y: 0, z: 0 },
        material: { type: "standard", color: "#888888" },
    });
    return scene;
};

const planBatch = {
    operations: [
        { section: "plan", path: ["name"], op: "add", value: "测试方案" },
        { section: "plan", path: ["duration"], op: "add", value: 2 },
        {
            section: "plan", path: ["tracks", "-"], op: "add", value: {
                objectId: "box1",
                frames: [
                    { time: 0, rotation: { x: 0, y: 0, z: 0 } },
                    { time: 2, rotation: { x: 0, y: 1, z: 0 } },
                ],
            },
        },
        {
            section: "plan", path: ["cameraFrames", "-"], op: "add", value: {
                time: 0, easing: "cut",
                view: {
                    camera: { position: { x: 10, y: 8, z: 12 }, fov: 55, near: 0.1, far: 1000 },
                    controls: { target: { x: 0, y: 0, z: 0 } },
                },
            },
        },
    ],
};

function scriptTransport(steps: DirectorAssistantMessage[], onCall?: (messages: DirectorMessage[], index: number) => void) {
    let index = 0;
    return async (messages: DirectorMessage[]) => {
        onCall?.(messages, index);
        const step = steps[Math.min(index, steps.length - 1)]!;
        index++;
        return step;
    };
}

const toolContents = (messages: DirectorMessage[]) =>
    messages.filter((m): m is DirectorToolResultMessage => m.role === "tool").map(m => m.content);
const lastToolContent = (messages: DirectorMessage[]) => toolContents(messages).at(-1)!;

describe("runDirectorTurn", () => {
    test("正常一轮：读取→编辑→完成，产出合法草稿且工具结果按 id 路由", async () => {
        const calls: DirectorMessage[][] = [];
        const result = await runDirectorTurn({
            scene: baseScene(),
            instruction: "让方块转起来",
            transport: scriptTransport([
                assistant("", [{ id: "c1", name: "readDocument", args: { section: "scene", objectId: "box1", path: ["position"] } }]),
                assistant("", [{ id: "c2", name: "editDocument", args: planBatch }]),
                assistant("已创建方案「测试方案」。"),
            ], messages => { calls.push([...messages]); }),
        });

        expect(result.status).toBe("completed");
        expect(result.rounds).toBe(2);
        expect(result.valid).toBe(true);
        expect(result.issues).toEqual([]);
        expect(result.draft?.plan.name).toBe("测试方案");
        expect(result.draft?.plan.tracks[0]!.objectId).toBe("box1");
        expect(result.draft?.plan.cameraFrames).toHaveLength(1);
        expect(result.draft?.scene.objectList[0]!.threeJsonId).toBe("box1");

        // 首轮消息 = system 提示词 + user 指令
        expect(calls[0]![0]!.role).toBe("system");
        expect(calls[0]![1]!.role).toBe("user");
        // 第二轮已带第一个工具结果，toolCallId 对齐
        expect(toolContents(calls[1]!)).toHaveLength(1);
        // readDocument 返回 position，且如实报告 plan 未完成
        const read = JSON.parse(toolContents(calls[1]!)![0]!) as { value: unknown; valid: boolean };
        expect(read.value).toEqual({ x: 0, y: 0, z: 0 });
        expect(read.valid).toBe(false);
    });

    test("工具参数非法→纠错循环，非法批次整批不落盘", async () => {
        const badBatch = {
            operations: [
                { section: "plan", path: ["name"], op: "add", value: "测试方案" },
                { section: "scene", objectId: "box1", path: ["position", "y"], op: "replace", value: 5 },
                { section: "scene", objectId: "box1", path: ["position", "ghost"], op: "replace", value: 1 },
            ],
        };
        let seen: DirectorMessage[] = [];
        const result = await runDirectorTurn({
            scene: baseScene(),
            instruction: "让方块转起来",
            transport: scriptTransport([
                assistant("", [{ id: "bad", name: "editDocument", args: badBatch }]),
                assistant("", [{ id: "fix", name: "editDocument", args: planBatch }]),
                assistant("修正完成。"),
            ], messages => { seen = [...messages]; }),
        });

        expect(result.status).toBe("completed");
        expect(result.valid).toBe(true);
        // 错误被原样回喂给模型纠错
        expect(toolContents(seen).some(content => content.includes("不存在"))).toBe(true);
        // 非法批次整体回滚：y 仍为初始 0，plan.name 由第二批次重新添加
        expect(result.draft?.scene.objectList[0]!.position.y).toBe(0);
        expect(result.draft?.plan.name).toBe("测试方案");
    });

    test("40 次工具轮触发熔断", async () => {
        let calls = 0;
        const result = await runDirectorTurn({
            scene: baseScene(),
            instruction: "一直读",
            transport: async () => {
                calls++;
                return assistant("", [{ id: `c${calls}`, name: "readDocument", args: { section: "schema", path: ["scene"] } }]);
            },
        });

        expect(result.status).toBe("circuit-breaker");
        expect(result.rounds).toBe(40);
        expect(calls).toBe(40);
        expect(result.valid).toBe(false);
        expect(result.draft).toBeUndefined();
    });

    test("外部 abort 立即终止且不再调用 transport", async () => {
        const controller = new AbortController();
        let calls = 0;
        const result = await runDirectorTurn({
            scene: baseScene(),
            instruction: "慢慢来",
            signal: controller.signal,
            transport: async () => {
                calls++;
                if (calls === 2) controller.abort();
                return assistant("", [{ id: `c${calls}`, name: "readDocument", args: { section: "schema" } }]);
            },
        });

        expect(result.status).toBe("aborted");
        expect(calls).toBe(2);
    });

    test("超时返回 timeout", async () => {
        const result = await runDirectorTurn({
            scene: baseScene(),
            instruction: "想清楚再答",
            timeoutMs: 5,
            transport: async () => {
                await Bun.sleep(50);
                return assistant("终于想好了");
            },
        });

        expect(result.status).toBe("timeout");
        expect(result.valid).toBe(false);
    });

    test("系统提示词注入，refs 按 {{ref N}} 编号进 user 消息", async () => {
        let seen: DirectorMessage[] = [];
        const result = await runDirectorTurn({
            scene: baseScene(),
            instruction: "坐到床边",
            refs: [{ kind: "image", content: "http://x/1.png" }, { kind: "text", content: "坐姿参考" }],
            transport: async messages => {
                seen = [...messages];
                return assistant("收到");
            },
        });

        expect(result.status).toBe("completed");
        expect(seen[0]!.role).toBe("system");
        expect(seen[0]!.role === "system" && seen[0]!.content).toContain("readDocument");
        expect(seen[1]!.role).toBe("user");
        expect(seen[1]!.role === "user" && seen[1]!.content).toContain("{{ref 1}} (image): http://x/1.png");
        expect(seen[1]!.role === "user" && seen[1]!.content).toContain("{{ref 2}} (text): 坐姿参考");
    });

    test("未知工具回错误提示；schema 区域可读出 scene/plan；transport 抛错返回 error", async () => {
        let seen: DirectorMessage[] = [];
        const unknown = await runDirectorTurn({
            scene: baseScene(),
            instruction: "跳个舞",
            transport: scriptTransport([
                assistant("", [{ id: "c1", name: "dance", args: {} }]),
                assistant("好的，我只用文档工具。"),
            ], messages => { seen = [...messages]; }),
        });
        expect(unknown.status).toBe("completed");
        expect(lastToolContent(seen)).toContain("未知工具");

        const schema = await runDirectorTurn({
            scene: baseScene(),
            instruction: "x",
            transport: scriptTransport([
                assistant("", [{ id: "s", name: "readDocument", args: { section: "schema", path: [] } }]),
                assistant("ok"),
            ], messages => { seen = [...messages]; }),
        });
        const schemaRead = JSON.parse(lastToolContent(seen)) as Record<string, unknown>;
        expect(Object.keys(schemaRead)).toEqual(["scene", "plan"]);

        const broken = await runDirectorTurn({
            scene: baseScene(),
            instruction: "x",
            transport: async () => { throw new Error("渠道断开"); },
        });
        expect(broken.status).toBe("error");
        expect(broken.message).toContain("渠道断开");
    });
});
