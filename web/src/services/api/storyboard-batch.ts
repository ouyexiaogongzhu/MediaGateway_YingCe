import { apiClient, request } from "@/services/api/request";
import type { GenerationTask } from "@/services/api/task-center";

// P11 分镜一键三件套：批量视频 / 批量音乐 / 合成成片。父任务统一走 GET /tasks/:id 轮询。

export type StoryboardShotPayload = {
    title?: string;
    description?: string;
    durationSeconds: number;
    dialogue?: string;
    shotSize?: string;
    emotion?: string;
    lightingAndAtmosphere?: string;
    audioEffects?: string;
    voiceMode?: string;
    sfxTags?: string[];
    musicGroupId?: string;
    musicMood?: string;
    visualPrompt?: string;
    videoPrompt?: string;
    camera?: string;
    motion?: string;
    timeBeats?: string;
    negativePrompt?: string;
    narrativeIntent?: string;
    viewerPOV?: string;
    performanceBlocking?: string;
    mustHave?: string[];
    optionalDetails?: string[];
    continuityOut?: string;
    characterIds?: string[];
};

export type StoryboardVideoBatchRowResult = {
    shotNumber: number;
    taskId?: string;
    status: string;
    error?: string;
    videoResourceId?: string;
};

export type StoryboardMusicSegment = {
    musicGroupId: string;
    resourceId: string;
    resourceUrl?: string;
    durationSeconds?: number;
};

export function createStoryboardVideoBatch(input: {
    projectId: string;
    rows: StoryboardShotPayload[];
    firstFrameResourceIds?: Record<string, string>;
    width?: number;
    height?: number;
}) {
    return request<GenerationTask>(apiClient.post("/storyboard-video-batch", input));
}

export function createStoryboardMusicBatch(input: { projectId: string; rows: StoryboardShotPayload[] }) {
    return request<GenerationTask>(apiClient.post("/storyboard-music-batch", input));
}

export function createStoryboardCompose(input: {
    projectId: string;
    rows: StoryboardShotPayload[];
    videoResourceIds: Record<string, string>;
    musicResourceIds: Record<string, string>;
    bgmGainDb?: number;
}) {
    return request<GenerationTask>(apiClient.post("/storyboard-compose", input));
}
