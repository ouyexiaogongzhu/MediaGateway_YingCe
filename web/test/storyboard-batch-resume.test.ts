import { describe, expect, test } from "bun:test";

import { batchTaskStillActive, pendingStoryboardMusicRows, pendingStoryboardVideoRows, shouldRetainBatchTaskId } from "../src/pages/canvas/use-canvas-storyboard";
import { ApiError } from "../src/services/api/request";
import { taskWaitTimeoutMs, TaskWaitTimeoutError, type GenerationTask } from "../src/services/api/task-center";
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

function task(status: string): GenerationTask {
    return { id: "t1", type: "storyboard_video_batch", status, prompt: "" } as unknown as GenerationTask;
}

describe("shouldRetainBatchTaskId", () => {
    // P0-4：清掉 taskId ＝ 按钮复原 ＝ 用户一按就重复烧 GPU。
    test("轮询超时必须留住 taskId（GPU 还在跑）", () => {
        expect(shouldRetainBatchTaskId(new TaskWaitTimeoutError("任务执行超时，请稍后重试"))).toBe(true);
    });

    test("页面跳转的 AbortError 也留住（Abort 不取消上游任务）", () => {
        expect(shouldRetainBatchTaskId(new DOMException("Aborted", "AbortError"))).toBe(true);
    });

    test("后端 404 = 查无此任务，可以放开重试", () => {
        expect(shouldRetainBatchTaskId(new ApiError("not found", { status: 404 }))).toBe(false);
    });

    test("任务自身失败：留 id 无害，重按闸门会按终态放行", () => {
        expect(shouldRetainBatchTaskId(new Error("任务失败"))).toBe(true);
    });

    test("断网/5xx 一律按『可能还在跑』处理（失败方向也要安全）", () => {
        expect(shouldRetainBatchTaskId(new ApiError("bad gateway", { status: 502 }))).toBe(true);
        expect(shouldRetainBatchTaskId(new TypeError("failed to fetch"))).toBe(true);
    });
});

describe("batchTaskStillActive", () => {
    test("排队中与运行中都算还活着，挡住重复提交", () => {
        expect(batchTaskStillActive(task("queued"))).toBe(true);
        expect(batchTaskStillActive(task("running"))).toBe(true);
    });

    test("终态放行：任务已经不在服务端跑了，重按安全", () => {
        expect(batchTaskStillActive(task("succeeded"))).toBe(false);
        expect(batchTaskStillActive(task("failed"))).toBe(false);
        expect(batchTaskStillActive(task("cancelled"))).toBe(false);
        expect(batchTaskStillActive(undefined)).toBe(false);
    });
});

describe("taskWaitTimeoutMs", () => {
    // 与 backend/internal/platform/runtime_policy.go 的 DB runtime_policy 对齐；
    // 客户端早于后端放弃 = 批次行冻结在进度 UI 里（image 10min 曾砍掉每批最慢的镜头）。
    function typed(type: string): GenerationTask {
        return { id: "t1", type, status: "running", prompt: "" } as unknown as GenerationTask;
    }
    const min = 60 * 1000;

    test("image 45min ≥ 后端上限（曾用 10min 提前放弃还在渲染的任务）", () => {
        expect(taskWaitTimeoutMs(typed("image_generation"))).toBe(45 * min);
    });

    test("video 用线上 DB 值 200min（源码默认 60）", () => {
        expect(taskWaitTimeoutMs(typed("video_generation"))).toBe(200 * min);
    });

    test("storyboard 20min / text・audio 8min / 未知 10min 与后端默认一致", () => {
        expect(taskWaitTimeoutMs(typed("storyboard_video_batch"))).toBe(20 * min);
        expect(taskWaitTimeoutMs(typed("text_generation"))).toBe(8 * min);
        expect(taskWaitTimeoutMs(typed("audio_generation"))).toBe(8 * min);
        expect(taskWaitTimeoutMs(typed("something_else"))).toBe(10 * min);
        expect(taskWaitTimeoutMs(undefined)).toBe(10 * min);
    });
});
