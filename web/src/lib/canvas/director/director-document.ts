import { nanoid } from "nanoid";
import { z } from "zod";

import type { DirectorLight, DirectorObject, DirectorScene, DirectorVec3, MannequinJoint } from "@/types/director";

import { createDirectorMannequin, createDirectorObject } from "./director-scene";
import { mannequinJoints } from "./director-mannequin";

export { mannequinJoints };
export type { MannequinJoint };

/**
 * Toonflow director3dNode 移植：场景 = zod 校验的 JSON 文档，AI 不碰 three.js，只发路径补丁。
 * 文档只描述 viewport 能原样渲染的子集（基础几何体 + 关节人偶）；GLB 演员/模型/看板不进文档，
 * applyDocumentToScene 时原样保留。geometry 是渲染尺寸的只读描述——要改尺寸请用 scale。
 */
const coordinate = z.number().min(-10000).max(10000);
const vector = z.strictObject({ x: coordinate, y: coordinate, z: coordinate });
const color = z.string().regex(/^#[\da-f]{6}$/i);

export const sceneSchema = z.strictObject({
    version: z.literal("next"),
    sceneConfig: z.strictObject({
        scene: z.strictObject({ background: color }),
        camera: z.strictObject({ position: vector, fov: z.number().min(10).max(120), near: z.number().min(0.01).max(10), far: z.number().min(20).max(10000) }),
        controls: z.strictObject({ target: vector }),
        lights: z.array(z.strictObject({
            type: z.enum(["ambient", "directional", "point", "spot", "hemisphere"]),
            color, intensity: z.number().min(0).max(100), position: vector.optional(), target: vector.optional(),
        })).max(16),
    }),
    objectList: z.array(z.strictObject({
        threeJsonId: z.string().trim().min(1).max(100), name: z.string().max(100).optional(),
        objType: z.enum(["box", "sphere", "cylinder", "cone", "ring", "torus", "capsule", "plane", "mannequin"]),
        geometry: z.record(z.string().regex(/^[a-zA-Z]+$/), z.union([z.number().min(0).max(10000), z.boolean()]))
            .refine(value => Object.entries(value).every(([key, size]) => !/segments/i.test(key) || (typeof size === "number" && Number.isInteger(size) && size >= 1 && size <= 64)), "细分段数必须是 1～64 的整数"),
        position: vector,
        rotation: z.strictObject({ rotationX: coordinate, rotationY: coordinate, rotationZ: coordinate }).optional(),
        scale: z.strictObject({ scaleX: coordinate, scaleY: coordinate, scaleZ: coordinate }).optional(),
        pose: z.partialRecord(z.enum(mannequinJoints), vector).describe("仅 mannequin：关节相对父关节的局部 XYZ 旋转，弧度，省略为自然站姿").optional(),
        hiddenParts: z.array(z.enum(mannequinJoints)).max(mannequinJoints.length).describe("仅 mannequin：不显示指定关节及其下游部位，省略为完整人偶").optional(),
        material: z.strictObject({
            type: z.enum(["standard", "basic", "phong", "lambert"]), color,
            roughness: z.number().min(0).max(1).optional(), metalness: z.number().min(0).max(1).optional(),
            opacity: z.number().min(0).max(1).optional(), transparent: z.boolean().optional(), wireframe: z.boolean().optional(),
        }),
    }).refine(object => object.objType === "mannequin" || (!object.pose && !object.hiddenParts), "仅人偶支持关节姿态和部位隐藏")).max(200).refine(objects => new Set(objects.map(object => object.threeJsonId)).size === objects.length, "物体 ID 不能重复"),
});
export type SceneDocument = z.infer<typeof sceneSchema>;
export const cameraViewSchema = sceneSchema.shape.sceneConfig.pick({ camera: true, controls: true });
export type CameraView = z.infer<typeof cameraViewSchema>;

/** 运镜文档：≤120 帧，时间严格递增；cut 表示硬切，smooth 为 smoothstep。 */
export const cameraFramesSchema = z.array(z.strictObject({
    time: z.number().min(0).max(300),
    view: cameraViewSchema,
    easing: z.enum(["linear", "smooth", "cut"]),
})).min(1).max(120).refine(frames => frames.every((frame, index) => !index || frame.time > frames[index - 1]!.time), "镜头时间必须递增");
export type CameraFrames = z.infer<typeof cameraFramesSchema>;

export const directorPlanSchema = z.strictObject({
    name: z.string().trim().min(1).max(80),
    duration: z.number().min(0.2).max(300),
    tracks: z.array(z.strictObject({
        objectId: z.string().trim().min(1).max(100),
        joint: z.enum(mannequinJoints).describe("仅人偶关节动画填写；不填时控制整个物体").optional(),
        frames: z.array(z.strictObject({
            time: z.number().min(0).max(300),
            position: vector.optional(),
            rotation: vector.describe("XYZ 欧拉角，单位为弧度"),
            scale: vector.optional(),
        })).min(2).max(120),
    })).max(200),
    cameraFrames: cameraFramesSchema,
}).superRefine((value, context) => {
    // 最后一个镜头保持到结束，无需重复添加结束帧；单帧表示固定镜头。
    if (value.cameraFrames[0]?.time !== 0 || value.cameraFrames.some(frame => frame.time > value.duration)) {
        context.addIssue({ code: "custom", path: ["cameraFrames"], message: "镜头时间必须从 0 开始，且不超过方案时长" });
    }
    const trackIds = new Set<string>();
    let frameCount = 0;
    value.tracks.forEach((track, index) => {
        const key = JSON.stringify([track.objectId, track.joint]);
        if (trackIds.has(key)) {
            context.addIssue({ code: "custom", path: ["tracks", index, "objectId"], message: "同一物体或关节只能有一条轨道" });
        }
        trackIds.add(key);
        if (track.joint && track.frames.some(frame => frame.position || frame.scale)) {
            context.addIssue({ code: "custom", path: ["tracks", index, "frames"], message: "关节动画仅填写 rotation，位置和缩放由骨架固定" });
        }
        frameCount += track.frames.length;
        if (track.frames[0]?.time !== 0 || track.frames.some((frame, frameIndex) => frame.time > value.duration || (frameIndex > 0 && frame.time <= track.frames[frameIndex - 1]!.time))) {
            context.addIssue({ code: "custom", path: ["tracks", index, "frames"], message: "关键帧时间必须从 0 开始、严格递增，且不超过场景时长" });
        }
    });
    if (frameCount > 2000) context.addIssue({ code: "custom", path: ["tracks"], message: "场景动画最多包含 2000 个关键帧" });
});
export type DirectorPlan = z.infer<typeof directorPlanSchema>;
export type DirectorPlanItem = DirectorPlan & { id: string; instruction?: string };
export type DirectorGeneration = { id: string; instruction: string; error?: string };

export function createMannequinObject(id: string): SceneDocument["objectList"][number] {
    return {
        threeJsonId: id, name: "关节人偶", objType: "mannequin", geometry: {},
        position: { x: 0, y: 0, z: 0 },
        material: { type: "standard", color: "#c7a77b", roughness: 0.75 },
    };
}

export function createEmptyScene(): SceneDocument {
    return {
        version: "next",
        sceneConfig: {
            scene: { background: "#202024" },
            camera: { position: { x: 10, y: 8, z: 12 }, fov: 55, near: 0.1, far: 1000 },
            controls: { target: { x: 0, y: 0, z: 0 } },
            lights: [
                { type: "ambient", color: "#ffffff", intensity: 0.8 },
                { type: "directional", color: "#ffffff", intensity: 2, position: { x: 10, y: 16, z: 12 } },
            ],
        },
        objectList: [],
    };
}

type DocumentObject = SceneDocument["objectList"][number];

const clamp = (value: number, min: number, max: number) => Math.min(max, Math.max(min, value));

function toVector(value: DirectorVec3) {
    return { x: value[0], y: value[1], z: value[2] };
}

function toVec3(value: { x: number; y: number; z: number } | undefined, fallback: DirectorVec3): DirectorVec3 {
    if (!value) return fallback;
    return [value.x, value.y, value.z];
}

/** viewport 基础几何体的固定尺寸（director-viewport.tsx 的 args），geometry 是它的只读描述。 */
function primitiveGeometry(primitive: NonNullable<DirectorObject["primitive"]>): DocumentObject["geometry"] {
    if (primitive === "sphere") return { radius: 0.6 };
    if (primitive === "cylinder") return { radius: 0.5, height: 1.2 };
    if (primitive === "plane") return { width: 1.6, height: 1 };
    return { width: 1, height: 1, depth: 1 };
}

function documentObjectFromScene(object: DirectorObject): DocumentObject | null {
    if (object.kind === "mannequin") {
        const poseEntries = Object.entries(object.jointPoses ?? {});
        return {
            threeJsonId: object.id, name: object.name, objType: "mannequin", geometry: {},
            position: toVector(object.transform.position),
            rotation: object.transform.rotation.some(Boolean) ? { rotationX: object.transform.rotation[0], rotationY: object.transform.rotation[1], rotationZ: object.transform.rotation[2] } : undefined,
            scale: object.transform.scale.some(value => value !== 1) ? { scaleX: object.transform.scale[0], scaleY: object.transform.scale[1], scaleZ: object.transform.scale[2] } : undefined,
            pose: poseEntries.length ? Object.fromEntries(poseEntries.map(([joint, euler]) => [joint, toVector(euler)])) : undefined,
            hiddenParts: object.hiddenParts?.length ? object.hiddenParts : undefined,
            material: { type: "standard", color: object.color },
        };
    }
    if (object.kind !== "primitive" || !object.primitive || object.primitive === "character") return null;
    return {
        threeJsonId: object.id, name: object.name, objType: object.primitive, geometry: primitiveGeometry(object.primitive),
        position: toVector(object.transform.position),
        rotation: object.transform.rotation.some(Boolean) ? { rotationX: object.transform.rotation[0], rotationY: object.transform.rotation[1], rotationZ: object.transform.rotation[2] } : undefined,
        scale: object.transform.scale.some(value => value !== 1) ? { scaleX: object.transform.scale[0], scaleY: object.transform.scale[1], scaleZ: object.transform.scale[2] } : undefined,
        material: { type: "standard", color: object.color },
    };
}

function activeCamera(scene: DirectorScene) {
    const shot = scene.shots.find(item => item.id === scene.activeShotId) ?? scene.shots[0];
    return scene.cameras.find(item => item.id === shot?.cameraId) ?? scene.cameras[0];
}

/** DirectorScene → AI 文档。只输出文档可表达的对象（基础几何体 + 人偶），先 parse 保证合法。 */
export function documentFromScene(scene: DirectorScene): SceneDocument {
    const camera = activeCamera(scene);
    const cameraView = camera
        ? {
            camera: { position: toVector(camera.transform.position), fov: clamp(camera.fov, 10, 120), near: clamp(camera.near, 0.01, 10), far: clamp(camera.far, 20, 10000) },
            controls: { target: toVector(camera.target) },
        }
        : createEmptyScene().sceneConfig;
    return sceneSchema.parse({
        version: "next",
        sceneConfig: {
            scene: { background: scene.background },
            camera: cameraView.camera,
            controls: cameraView.controls,
            lights: scene.lights.map(light => ({
                type: light.type,
                color: light.color,
                intensity: clamp(light.intensity, 0, 100),
                ...(light.type !== "ambient" ? { position: toVector(light.transform.position) } : {}),
            })),
        },
        objectList: scene.objects.map(documentObjectFromScene).filter(item => item !== null),
    });
}

function createDocumentObject(doc: DocumentObject): DirectorObject {
    const transform = {
        position: toVec3(doc.position, [0, 0, 0]),
        rotation: [doc.rotation?.rotationX ?? 0, doc.rotation?.rotationY ?? 0, doc.rotation?.rotationZ ?? 0] as DirectorVec3,
        scale: [doc.scale?.scaleX ?? 1, doc.scale?.scaleY ?? 1, doc.scale?.scaleZ ?? 1] as DirectorVec3,
    };
    if (doc.objType === "mannequin") {
        return {
            ...createDirectorMannequin(doc.name ?? "人偶", transform.position, doc.material.color),
            id: doc.threeJsonId,
            transform,
            jointPoses: Object.fromEntries(Object.entries(doc.pose ?? {}).map(([joint, euler]) => [joint, toVec3(euler, [0, 0, 0])])) as Partial<Record<MannequinJoint, DirectorVec3>>,
            hiddenParts: doc.hiddenParts ?? [],
        };
    }
    if (doc.objType === "box" || doc.objType === "sphere" || doc.objType === "cylinder" || doc.objType === "plane") {
        return { ...createDirectorObject(doc.objType, doc.name ?? "新对象", transform.position, doc.material.color), id: doc.threeJsonId, transform };
    }
    // cone/ring/torus/capsule：schema 允许但 viewport 无对应渲染，落成最接近的既有几何体。
    return { ...createDirectorObject("sphere", doc.name ?? "新对象", transform.position, doc.material.color), id: doc.threeJsonId, transform };
}

function mergeDocumentObject(existing: DirectorObject, doc: DocumentObject): DirectorObject {
    const transform = {
        position: toVec3(doc.position, existing.transform.position),
        rotation: [doc.rotation?.rotationX ?? 0, doc.rotation?.rotationY ?? 0, doc.rotation?.rotationZ ?? 0] as DirectorVec3,
        scale: [doc.scale?.scaleX ?? 1, doc.scale?.scaleY ?? 1, doc.scale?.scaleZ ?? 1] as DirectorVec3,
    };
    const kindMatches = existing.kind === "mannequin" ? doc.objType === "mannequin" : existing.kind === "primitive" && existing.primitive === doc.objType;
    if (!kindMatches) return createDocumentObject(doc);
    return {
        ...existing,
        name: doc.name ?? existing.name,
        transform,
        color: doc.material.color,
        ...(existing.kind === "mannequin" ? {
            jointPoses: Object.fromEntries(Object.entries(doc.pose ?? {}).map(([joint, euler]) => [joint, toVec3(euler, [0, 0, 0])])) as Partial<Record<MannequinJoint, DirectorVec3>>,
            hiddenParts: doc.hiddenParts ?? [],
        } : {}),
    };
}

/**
 * AI 文档 → DirectorScene。文档未描述的字段（keyframes、visible、GLB 演员等）原样保留；
 * 灯光按序号对应（Toonflow 文档灯无 id），可映射类型之外的 hemisphere 不落回场景。
 */
export function applyDocumentToScene(scene: DirectorScene, document: SceneDocument): DirectorScene {
    const docObjects = new Map(document.objectList.map(item => [item.threeJsonId, item]));
    const objects: DirectorObject[] = scene.objects.map(object => {
        const doc = docObjects.get(object.id);
        return doc ? mergeDocumentObject(object, doc) : object;
    });
    for (const doc of document.objectList) {
        if (scene.objects.some(object => object.id === doc.threeJsonId)) continue;
        objects.push(createDocumentObject(doc));
    }
    const lights: DirectorLight[] = document.sceneConfig.lights.flatMap((docLight, index) => {
        if (docLight.type === "hemisphere") return [];
        const existing = scene.lights[index];
        const transform = existing?.transform ?? { position: toVec3(docLight.position, [0, 0, 0]), rotation: [0, 0, 0] as DirectorVec3, scale: [1, 1, 1] as DirectorVec3 };
        return [{
            id: existing?.id ?? nanoid(),
            name: existing?.name ?? `灯光 ${index + 1}`,
            type: docLight.type,
            transform: docLight.type === "ambient" ? transform : { ...transform, position: toVec3(docLight.position, transform.position) },
            color: docLight.color,
            intensity: docLight.intensity,
            angle: existing?.angle ?? Math.PI / 4,
            penumbra: existing?.penumbra ?? 0.35,
            castShadow: existing?.castShadow ?? docLight.type !== "ambient",
        }];
    });
    const camera = activeCamera(scene);
    const cameras = camera
        ? scene.cameras.map(item => item.id === camera.id ? {
            ...item,
            transform: { ...item.transform, position: toVec3(document.sceneConfig.camera.position, item.transform.position) },
            target: toVec3(document.sceneConfig.controls.target, item.target),
            fov: document.sceneConfig.camera.fov,
            near: document.sceneConfig.camera.near,
            far: document.sceneConfig.camera.far,
        } : item)
        : scene.cameras;
    return { ...scene, objects, lights, cameras };
}
