import assert from "node:assert/strict";
import test from "node:test";

import type { DirectorVec3 } from "@/types/director";

import { prepareDirectorPlan, sampleDirectorPlan, type PlanObjectBase } from "./director-animation";
import type { DirectorPlan } from "./director-document";

const base = (position: DirectorVec3): PlanObjectBase => ({ position, scale: [1, 1, 1] });

const view = (position: [number, number, number], target: [number, number, number], easing: "linear" | "smooth" | "cut" = "linear") => ({
    time: 0,
    easing,
    view: {
        camera: { position: { x: position[0], y: position[1], z: position[2] }, fov: 50, near: 0.05, far: 500 },
        controls: { target: { x: target[0], y: target[1], z: target[2] } },
    },
});

/** 「走两步+推镜」：整体轨道 z 0→2.4，左髋摆动，相机 5→2.6 线性推近。 */
function walkPlan(): DirectorPlan {
    return {
        name: "走两步+推镜",
        duration: 4,
        tracks: [
            {
                objectId: "m1",
                frames: [
                    { time: 0, rotation: { x: 0, y: 0, z: 0 }, position: { x: 0, y: 0, z: 0 } },
                    { time: 2, rotation: { x: 0, y: 0, z: 0 }, position: { x: 0, y: 0, z: 1.2 } },
                    { time: 4, rotation: { x: 0, y: 0, z: 0 }, position: { x: 0, y: 0, z: 2.4 } },
                ],
            },
            {
                objectId: "m1",
                joint: "leftHip",
                frames: [
                    { time: 0, rotation: { x: -0.3, y: 0, z: 0 } },
                    { time: 1, rotation: { x: 0.3, y: 0, z: 0 } },
                ],
            },
        ],
        cameraFrames: [
            { ...view([0, 1.5, 5], [0, 1, 0]), time: 0 },
            { ...view([0, 1.4, 2.6], [0, 1, 1]), time: 4 },
        ],
    };
}

const bases = new Map<string, PlanObjectBase>([["m1", base([0, 0, 0])]]);

test("prepare+sample 复现「走两步+推镜」的首/中/尾帧", () => {
    const prepared = prepareDirectorPlan(walkPlan(), (id) => bases.get(id));

    const start = sampleDirectorPlan(prepared, 0);
    assert.deepEqual(start.objects.get("m1")!.position, [0, 0, 0]);
    assert.equal(start.camera.camera.position.z, 5);
    assert.equal(start.finished, false);

    const mid = sampleDirectorPlan(prepared, 2);
    assert.deepEqual(mid.objects.get("m1")!.position, [0, 0, 1.2]);
    // 关节轨道 t=2 在 1→1 帧区间之后：保持末帧 0.3（四元数往返欧拉有 1e-16 级噪声，近似比较）。
    const midHip = mid.joints.get("m1")!.leftHip!;
    assert.ok(Math.abs(midHip[0] - 0.3) < 1e-9 && Math.abs(midHip[1]) < 1e-9 && Math.abs(midHip[2]) < 1e-9, `leftHip=${midHip}`);
    // 相机线性推进：z 5→2.6 的一半在 t=2 → 3.8。
    assert.ok(Math.abs(mid.camera.camera.position.z - 3.8) < 1e-9);

    const end = sampleDirectorPlan(prepared, 4);
    assert.deepEqual(end.objects.get("m1")!.position, [0, 0, 2.4]);
    assert.equal(end.camera.camera.position.z, 2.6);
    assert.equal(end.finished, true);
});

test("关节插值在关键帧之间线性过零", () => {
    const prepared = prepareDirectorPlan(walkPlan(), (id) => bases.get(id));
    const half = sampleDirectorPlan(prepared, 0.5);
    const hip = half.joints.get("m1")!.leftHip!;
    assert.ok(Math.abs(hip[0]) < 1e-9, `t=0.5 应过零，实际 ${hip[0]}`);
});

test("超时保持末帧（hold），早于首帧保持首帧", () => {
    const prepared = prepareDirectorPlan(walkPlan(), (id) => bases.get(id));
    const beyond = sampleDirectorPlan(prepared, 99);
    assert.deepEqual(beyond.objects.get("m1")!.position, [0, 0, 2.4]);
    assert.equal(beyond.camera.camera.position.z, 2.6);
    const before = sampleDirectorPlan(prepared, -1);
    assert.deepEqual(before.objects.get("m1")!.position, [0, 0, 0]);
});

test("只旋转的整体轨道不丢基础位置与缩放", () => {
    const plan = walkPlan();
    plan.tracks = [{
        objectId: "m2",
        frames: [
            { time: 0, rotation: { x: 0, y: 0, z: 0 } },
            { time: 4, rotation: { x: 0, y: Math.PI, z: 0 } },
        ],
    }];
    const prepared = prepareDirectorPlan(plan, (id) => bases.get(id) ?? base([1, 0, -2]));
    const sample = sampleDirectorPlan(prepared, 2);
    const object = sample.objects.get("m2")!;
    assert.deepEqual(object.position, [1, 0, -2]);
    assert.deepEqual(object.scale, [1, 1, 1]);
    assert.ok(Math.abs(object.rotation[1] - Math.PI / 2) < 1e-6, `yaw=${object.rotation[1]}`);
});

test("整体轨道缺省位置回填物体当前值（Toonflow 语义）", () => {
    const plan = walkPlan();
    plan.tracks = [{
        objectId: "m1",
        frames: [
            { time: 0, rotation: { x: 0, y: 0, z: 0 } },
            { time: 4, rotation: { x: 0, y: 0, z: 0 }, position: { x: 3, y: 0, z: 0 } },
        ],
    }];
    const prepared = prepareDirectorPlan(plan, () => base([1, 0.5, -2]));
    const start = sampleDirectorPlan(prepared, 0);
    assert.deepEqual(start.objects.get("m1")!.position, [1, 0.5, -2]);
});

test("找不到整体轨道的物体时 prepare 报错", () => {
    const plan = walkPlan();
    plan.tracks = [{ objectId: "ghost", frames: [
        { time: 0, rotation: { x: 0, y: 0, z: 0 }, position: { x: 0, y: 0, z: 0 } },
        { time: 4, rotation: { x: 0, y: 0, z: 0 }, position: { x: 0, y: 0, z: 1 } },
    ] }];
    assert.throws(() => prepareDirectorPlan(plan, () => undefined), /ghost/);
});
