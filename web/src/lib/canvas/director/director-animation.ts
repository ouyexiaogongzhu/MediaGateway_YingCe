import { Euler, PerspectiveCamera, Quaternion, Vector3 } from "three";

import type { DirectorVec3, MannequinJoint } from "@/types/director";

import { directorPlanSchema, type CameraView, type DirectorPlan } from "./director-document";
import { prepareMotion, sampleMotion } from "./director-motion";

/**
 * Toonflow sceneAnimation.ts 移植：方案 = 按 objectId/joint 的轨道 + 运镜轨道。
 * Toonflow 用 AnimationMixer 绑定 threejson 运行时对象；影策视口是声明式渲染，
 * 这里改为纯采样（同样的插值语义：旋转逐帧欧拉→四元数后 slerp，位置/缩放 lerp，
 * 与 three QuaternionKeyframeTrack 一致），由视口按 playhead 取值。
 */

export type PlanObjectBase = { position: DirectorVec3; scale: DirectorVec3 };

export type PreparedPlanTrack = {
    objectId: string;
    joint?: MannequinJoint;
    times: number[];
    rotations: Quaternion[];
    /** 编译期把缺省帧回填为物体当时的位置/缩放（Toonflow 的 `frame[property] ?? object[property]`）。 */
    positions: Vector3[] | null;
    scales: Vector3[] | null;
    basePosition: Vector3;
    baseScale: Vector3;
};

export type PreparedPlan = {
    duration: number;
    tracks: PreparedPlanTrack[];
    cameraFrames: ReturnType<typeof prepareMotion>;
};

export function prepareDirectorPlan(plan: DirectorPlan, baseOf: (objectId: string) => PlanObjectBase | undefined): PreparedPlan {
    const validated = directorPlanSchema.parse(plan);
    return {
        duration: validated.duration,
        tracks: validated.tracks.map(track => {
            if (track.joint) {
                // 关节轨道只有 rotation；骨架由人偶自带，不查物体，position/scale 恒空。
                return {
                    objectId: track.objectId,
                    joint: track.joint,
                    times: track.frames.map(frame => frame.time),
                    rotations: track.frames.map(frame => new Quaternion().setFromEuler(new Euler(frame.rotation.x, frame.rotation.y, frame.rotation.z))),
                    positions: null,
                    scales: null,
                    basePosition: new Vector3(),
                    baseScale: new Vector3(1, 1, 1),
                };
            }
            const base = baseOf(track.objectId);
            // 只有整体轨道需要 base 回填（Toonflow 的 `frame[property] ?? object[property]`）。
            if (!base) throw new Error(`场景中找不到动画物体：${track.objectId}`);
            return {
                objectId: track.objectId,
                times: track.frames.map(frame => frame.time),
                rotations: track.frames.map(frame => new Quaternion().setFromEuler(new Euler(frame.rotation.x, frame.rotation.y, frame.rotation.z))),
                positions: track.frames.some(frame => frame.position) ? track.frames.map(frame => frame.position ? new Vector3(frame.position.x, frame.position.y, frame.position.z) : new Vector3(...base.position)) : null,
                scales: track.frames.some(frame => frame.scale) ? track.frames.map(frame => frame.scale ? new Vector3(frame.scale.x, frame.scale.y, frame.scale.z) : new Vector3(...base.scale)) : null,
                basePosition: new Vector3(...base.position),
                baseScale: new Vector3(...base.scale),
            };
        }),
        cameraFrames: prepareMotion(validated.cameraFrames),
    };
}

export type PlanObjectSample = { position: DirectorVec3; rotation: DirectorVec3; scale: DirectorVec3 };
export type PlanJointSample = Partial<Record<MannequinJoint, DirectorVec3>>;

export type DirectorPlanSample = {
    /** 整体轨道（ objectId → transform 覆盖值）。 */
    objects: Map<string, PlanObjectSample>;
    /** 关节轨道（ objectId → joint → 欧拉弧度），覆盖 mannequin 的 jointPoses。 */
    joints: Map<string, PlanJointSample>;
    camera: CameraView;
    finished: boolean;
};

/** times 严格递增（schema 保证）；首尾帧保持（hold），中间线性。 */
function trackSpan(times: number[], time: number) {
    let index = 0;
    while (index < times.length - 1 && times[index + 1]! <= time) index += 1;
    const next = Math.min(index + 1, times.length - 1);
    const span = times[next]! - times[index]!;
    return { index, next, progress: span > 0 ? Math.min(1, Math.max(0, (time - times[index]!) / span)) : 1 };
}

function lerpVec3(from: Vector3, to: Vector3, progress: number): DirectorVec3 {
    return [from.x + (to.x - from.x) * progress, from.y + (to.y - from.y) * progress, from.z + (to.z - from.z) * progress];
}

export function sampleDirectorPlan(prepared: PreparedPlan, time: number): DirectorPlanSample {
    const clamped = Math.min(prepared.duration, Math.max(0, time));
    const objects = new Map<string, { position: DirectorVec3; rotation: DirectorVec3; scale: DirectorVec3 }>();
    const joints = new Map<string, Partial<Record<MannequinJoint, DirectorVec3>>>();
    const quaternion = new Quaternion();
    const euler = new Euler();
    for (const track of prepared.tracks) {
        const { index, next, progress } = trackSpan(track.times, clamped);
        quaternion.slerpQuaternions(track.rotations[index]!, track.rotations[next]!, progress);
        euler.setFromQuaternion(quaternion);
        const rotation: DirectorVec3 = [euler.x, euler.y, euler.z];
        if (track.joint) {
            joints.set(track.objectId, { ...joints.get(track.objectId), [track.joint]: rotation });
            continue;
        }
        objects.set(track.objectId, {
            position: track.positions ? lerpVec3(track.positions[index]!, track.positions[next]!, progress) : track.basePosition.toArray() as DirectorVec3,
            rotation,
            scale: track.scales ? lerpVec3(track.scales[index]!, track.scales[next]!, progress) : track.baseScale.toArray() as DirectorVec3,
        });
    }
    // 运镜：复用 Toonflow sampleMotion 的相机 slerp（position/quaternion/fov-near-far），
    // 再从采样相机读回 CameraView（target = position + 世界前向），与 captureCamera 互逆。
    const scratch = new PerspectiveCamera();
    const motionFinished = sampleMotion(scratch, prepared.cameraFrames, clamped);
    const target = scratch.getWorldDirection(new Vector3()).add(scratch.position);
    return {
        objects,
        joints,
        camera: {
            camera: { position: { x: scratch.position.x, y: scratch.position.y, z: scratch.position.z }, fov: scratch.fov, near: scratch.near, far: scratch.far },
            controls: { target: { x: target.x, y: target.y, z: target.z } },
        },
        finished: motionFinished && clamped >= prepared.duration,
    };
}
