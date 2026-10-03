import { describe, expect, test } from "bun:test";

import { pendingStoryboardMusicRows, pendingStoryboardVideoRows } from "../src/pages/canvas/use-canvas-storyboard";
import type { StoryboardRow } from "../src/types/canvas";

function row(id: string, musicGroupId?: string): StoryboardRow {
    return { id, shotNumber: 1, musicGroupId } as StoryboardRow;
}

describe("pendingStoryboardVideoRows", () => {
    const rows = [row("a"), row("b"), row("c")];

    test("没有历史结果时全部入队", () => {
        expect(pendingStoryboardVideoRows(rows)).toHaveLength(3);
    });

    test("已出片的行不再重排队（误按一次曾重跑全部镜头）", () => {
        const previous = {
            a: { status: "succeeded", videoResourceId: "res-a" },
            b: { status: "succeeded", videoResourceId: "res-b" },
        };
        expect(pendingStoryboardVideoRows(rows, previous).map((r) => r.id)).toEqual(["c"]);
    });

    test("失败的行仍会重试", () => {
        const previous = { a: { status: "failed", error: "boom" }, b: { status: "succeeded", videoResourceId: "res-b" } };
        expect(pendingStoryboardVideoRows(rows, previous).map((r) => r.id)).toEqual(["a", "c"]);
    });

    test("status 成功但没落资源库 = 未完成，必须重跑", () => {
        const previous = { a: { status: "succeeded" } };
        expect(pendingStoryboardVideoRows(rows, previous).map((r) => r.id)).toEqual(["a", "b", "c"]);
    });
});

describe("pendingStoryboardMusicRows", () => {
    test("已有配乐的段整组跳过", () => {
        const rows = [row("a", "g1"), row("b", "g1"), row("c", "g2")];
        const previous = [{ musicGroupId: "g1", resourceId: "res-1" }];
        expect(pendingStoryboardMusicRows(rows, previous).map((r) => r.id)).toEqual(["c"]);
    });

    test("没有 resourceId 的段不算完成", () => {
        const rows = [row("a", "g1")];
        expect(pendingStoryboardMusicRows(rows, [{ musicGroupId: "g1" }])).toHaveLength(1);
    });

    test("缺省段名 seg-01 与后端一致", () => {
        const rows = [row("a"), row("b")];
        expect(pendingStoryboardMusicRows(rows, [{ musicGroupId: "seg-01", resourceId: "res-1" }])).toHaveLength(0);
    });
});