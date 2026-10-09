import assert from "node:assert/strict";
import test from "node:test";

import type { DirectorVec3 } from "@/types/director";

import { applyDocumentToScene, createMannequinObject, directorPlanSchema, documentFromScene, mannequinJoints, sceneSchema, type DirectorPlan } from "./director-document";
import { createMannequin } from "./director-mannequin";
import { createDirectorMannequin, createDirectorObject, createDirectorScene } from "./director-scene";

function vector(x: number, y: number, z: number) {
    return { x, y, z };
}

const cameraView = (position: [number, number, number], target: [number, number, number]) => ({
    camera: { position: vector(...position), fov: 50, near: 0.05, far: 500 },
    controls: { target: vector(...target) },
});

function sampleScene() {
    const scene = createDirectorScene("测试场景");
    const mannequinA = createDirectorMannequin("女偶 A", [1, 0, 0], "#c7a77b");
    mannequinA.jointPoses = { leftKnee: [0.5, 0, 0], rightWrist: [0, 0.2, 0.1] };
    const box = createDirectorObject("box", "箱子", [2, 0.5, -1], "#8795a5");
    box.transform = { position: [2, 0.5, -1], rotation: [0, 0.3, 0], scale: [2, 1, 1] };
    scene.objects.push(mannequinA, createDirectorMannequin("人偶 B", [-1, 0, 0], "#d84949"), createDirectorObject("sphere", "球", [0, 0.6, 2], "#34a276"), box);
    return scene;
}

test("mannequin 几何体包含全部 16 个命名关节", () => {
    const group = createMannequin("m1");
    assert.equal(group.name, "mannequin:m1");
    for (const joint of mannequinJoints) assert.ok(group.getObjectByName(joint), `缺少关节 ${joint}`);
});

test("sceneSchema 接受人偶+几何体文档，拒绝重复 ID 与非人偶关节姿态", () => {
    const doc = documentFromScene(sampleScene());
    assert.equal(sceneSchema.safeParse(doc).success, true);

    const duplicate = structuredClone(doc);
    duplicate.objectList.push({ ...duplicate.objectList[0]! });
    assert.equal(sceneSchema.safeParse(duplicate).success, false);

    const posedBox = structuredClone(doc);
    const box = posedBox.objectList.find((item) => item.objType === "box")!;
    box.pose = { hips: vector(0, 0, 0) };
    assert.equal(sceneSchema.safeParse(posedBox).success, false);

    const badSegments = structuredClone(doc);
    badSegments.objectList[0]!.geometry = { radiusSegments: 100 };
    assert.equal(sceneSchema.safeParse(badSegments).success, false);
});

test("DirectorScene → 文档 → DirectorScene round-trip 无损", () => {
    const scene = sampleScene();
    const doc = documentFromScene(scene);
    const restored = applyDocumentToScene(scene, doc);

    // 文档幂等：还原后的场景再导一次，文档必须完全一致。
    assert.deepEqual(documentFromScene(restored), doc);

    // 对象数量不变；文档未表达的 GLB 演员原样保留（引用不变）。
    assert.equal(restored.objects.length, scene.objects.length);
    const actor = scene.objects.find((item) => item.kind === "actor")!;
    assert.equal(restored.objects.find((item) => item.id === actor.id), actor);

    // 人偶姿态与缩放写回。
    const merged = restored.objects.find((item) => item.name === "女偶 A")!;
    assert.deepEqual(merged.jointPoses?.leftKnee, [0.5, 0, 0]);
    assert.deepEqual(merged.jointPoses?.rightWrist, [0, 0.2, 0.1]);
    const box = restored.objects.find((item) => item.name === "箱子")!;
    assert.deepEqual(box.transform.scale, [2, 1, 1] as DirectorVec3);

    // 灯光保持文档外的字段（阴影/角度），相机参数写回活动镜头的摄影机。
    assert.deepEqual(restored.lights.map((light) => [light.type, light.castShadow]), scene.lights.map((light) => [light.type, light.castShadow]));
    assert.equal(restored.cameras[0]!.fov, doc.sceneConfig.camera.fov);
    assert.deepEqual(restored.cameras[0]!.target, [doc.sceneConfig.controls.target.x, doc.sceneConfig.controls.target.y, doc.sceneConfig.controls.target.z]);
});

test("文档中的新增对象（AI 添加）落回场景为人偶", () => {
    const scene = sampleScene();
    const doc = documentFromScene(scene);
    doc.objectList.push({ ...createMannequinObject("brand-new-id"), position: vector(3, 0, 3) });
    const expanded = applyDocumentToScene(scene, doc);
    const added = expanded.objects.find((item) => item.id === "brand-new-id");
    assert.ok(added);
    assert.equal(added!.kind, "mannequin");
    assert.equal(added!.color, "#c7a77b");
});

test("directorPlanSchema 接受「走两步+推镜」合法方案", () => {
    const plan: DirectorPlan = {
        name: "走两步+推镜",
        duration: 4,
        tracks: [
            {
                objectId: "m1",
                frames: [
                    { time: 0, rotation: vector(0, 0, 0), position: vector(0, 0, 0) },
                    { time: 2, rotation: vector(0, 0, 0), position: vector(0, 0, 1.2) },
                    { time: 4, rotation: vector(0, 0, 0), position: vector(0, 0, 2.4) },
                ],
            },
            {
                objectId: "m1",
                joint: "leftHip",
                frames: [
                    { time: 0, rotation: vector(-0.3, 0, 0) },
                    { time: 1, rotation: vector(0.3, 0, 0) },
                    { time: 2, rotation: vector(-0.3, 0, 0) },
                ],
            },
        ],
        cameraFrames: [
            { time: 0, easing: "linear", view: cameraView([0, 1.5, 5], [0, 1, 0]) },
            { time: 4, easing: "smooth", view: cameraView([0, 1.4, 2.5], [0, 1, 0]) },
        ],
    };
    assert.equal(directorPlanSchema.safeParse(plan).success, true);
});

test("directorPlanSchema 拒绝不合规方案", () => {
    const base = (): DirectorPlan => ({
        name: "方案",
        duration: 4,
        tracks: [{
            objectId: "m1",
            frames: [
                { time: 0, rotation: vector(0, 0, 0), position: vector(0, 0, 0) },
                { time: 2, rotation: vector(0, 0, 0), position: vector(0, 0, 1) },
            ],
        }],
        cameraFrames: [
            { time: 0, easing: "linear", view: cameraView([0, 1.5, 5], [0, 1, 0]) },
            { time: 4, easing: "linear", view: cameraView([0, 1.5, 3], [0, 1, 0]) },
        ],
    });

    const cameraNotFromZero = base();
    cameraNotFromZero.cameraFrames[0]!.time = 1;
    assert.equal(directorPlanSchema.safeParse(cameraNotFromZero).success, false);

    const cameraBeyondDuration = base();
    cameraBeyondDuration.cameraFrames[1]!.time = 5;
    assert.equal(directorPlanSchema.safeParse(cameraBeyondDuration).success, false);

    const jointWithPosition = base();
    jointWithPosition.tracks.push({
        objectId: "m1",
        joint: "leftKnee",
        frames: [
            { time: 0, rotation: vector(0, 0, 0), position: vector(1, 0, 0) },
            { time: 2, rotation: vector(0, 0, 0), position: vector(1, 0, 0) },
        ],
    });
    assert.equal(directorPlanSchema.safeParse(jointWithPosition).success, false);

    const duplicateTrack = base();
    duplicateTrack.tracks.push({
        objectId: "m1",
        joint: "leftKnee",
        frames: [
            { time: 0, rotation: vector(0, 0, 0) },
            { time: 2, rotation: vector(0.2, 0, 0) },
        ],
    });
    duplicateTrack.tracks.push({
        objectId: "m1",
        joint: "leftKnee",
        frames: [
            { time: 0, rotation: vector(0, 0, 0) },
            { time: 2, rotation: vector(0.4, 0, 0) },
        ],
    });
    assert.equal(directorPlanSchema.safeParse(duplicateTrack).success, false);

    const frameNotIncreasing = base();
    frameNotIncreasing.tracks[0]!.frames[1]!.time = 0;
    assert.equal(directorPlanSchema.safeParse(frameNotIncreasing).success, false);
});
