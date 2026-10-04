import { describe, expect, test } from "bun:test";

import { autoBindStoryboardRowAssets } from "@/lib/canvas/canvas-storyboard-assets";
import { CanvasNodeType, type CanvasNodeData } from "@/types/canvas";

const img = (id: string, title: string): CanvasNodeData =>
    ({ id, type: CanvasNodeType.Image, title, position: { x: 0, y: 0 }, metadata: {} }) as CanvasNodeData;

const nodes = [img("image-a", "冰魔法師"), img("image-b", "火魔法師"), img("image-c", "冰火")];

describe("autoBindStoryboardRowAssets — 死綁定剔除", () => {
    // 模型寫 assetRefs、前端讀 assetBindings，形狀相同但欄位名不同 —— 這段映射
    // 缺席時，下游「剔除死綁定」整套邏輯對模型輸出完全失效。
    test("剔除指向不存在節點的死綁定（模型自創名字）", () => {
        const rows = autoBindStoryboardRowAssets(
            [{
                characters: [],
                plotDescription: "",
                assetBindings: [
                    { nodeId: "角色2", role: "character", priority: 100 },
                    { nodeId: "场景1", role: "environment", priority: 100 },
                ],
            }] as never[],
            nodes,
        );
        expect(rows[0].assetBindings ?? []).toEqual([]);
    });

    test("已存在的 assetBindings 不被覆蓋", () => {
        const rows = autoBindStoryboardRowAssets(
            [{
                characters: [],
                plotDescription: "",
                assetBindings: [
                    { nodeId: "image-a", role: "character", priority: 60 },
                    { nodeId: "image-b", role: "character", priority: 100 },
                ],
            }] as never[],
            nodes,
        );
        expect(rows[0].assetBindings?.map((b) => b.nodeId).sort()).toEqual(["image-a", "image-b"]);
    });

    test("沒有綁定時行為不變（回歸）", () => {
        const rows = autoBindStoryboardRowAssets(
            [{ characters: [{ characterName: "冰魔法師霜璃" }], plotDescription: "霜璃 揮劍" }] as never[],
            nodes,
        );
        expect(rows[0].assetBindings?.map((b) => b.nodeId)).toContain("image-a");
    });

    test("剔除死綁定後仍會用標題詞元補回正確的角色", () => {
        // 標題匹配的規則是「角色名包含節點標題」，所以角色名要帶上前綴：
        // 畫布上圖片節點叫「火魔法師」，LLM 給的角色名是「火魔法師烬炎」。
        const rows = autoBindStoryboardRowAssets(
            [{
                characters: [{ characterName: "火魔法師烬炎" }],
                plotDescription: "烬炎 抬手",
                assetRefs: [{ nodeId: "角色2", role: "character", priority: 100 }],
            }] as never[],
            nodes,
        );
        const ids = rows[0].assetBindings?.map((b) => b.nodeId) ?? [];
        expect(ids).not.toContain("角色2");
        expect(ids).toContain("image-b"); // 火魔法師
    });
});
describe("scriptAssetHints — 兩種提及寫法", () => {
    const hintRows = (scriptText: string, rows: object[], refs: unknown[]) =>
        autoBindStoryboardRowAssets(rows as never[], nodes, { scriptText, mentionRefs: refs as never });

    const refs = [
        { kind: "image", nodeId: "image-c", label: "@图片1" },
        { kind: "image", nodeId: "image-b", label: "@图片2" },
        { kind: "image", nodeId: "image-a", label: "@图片3" },
    ];

    test("提及在前：@图片2 是火魔法師烬炎", () => {
        const rows = hintRows("@图片1 是場景圖\n@图片3 是冰魔法師霜璃\n@图片2 是火魔法師烬炎\n", [
            { characters: [{ characterName: "烬炎 (火法师)" }], plotDescription: "烬炎 抬手" },
        ], refs);
        expect(rows[0].assetBindings?.map((b) => b.nodeId)).toContain("image-b");
    });

    test("名字在前：火魔法師烬炎（@图片2）仍然認", () => {
        const rows = hintRows("火魔法師烬炎（@图片2）", [
            { characters: [{ characterName: "烬炎 (火法师)" }], plotDescription: "烬炎 抬手" },
        ], refs);
        expect(rows[0].assetBindings?.map((b) => b.nodeId)).toContain("image-b");
    });

    test("場景圖 → environment（去掉尾字「圖」）", () => {
        const rows = hintRows("@图片1 是場景图", [{ characters: [], plotDescription: "雪原" }], refs);
        const binding = rows[0].assetBindings?.find((b) => b.nodeId === "image-c");
        expect(binding?.role).toBe("environment");
    });
});
