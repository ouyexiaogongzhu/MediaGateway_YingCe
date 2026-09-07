import { useEffect, useMemo, useState } from "react";
import { Alert, Button, Card, Progress, Radio, message } from "antd";
import { Download, Library, MonitorUp, X } from "lucide-react";

import { AssetLibraryPickerModal, type AssetLibraryPickerItem } from "@/components/assets/asset-library-picker-modal";
import { resourceIdFromStorageKey } from "@/services/api/resources";
import { apiBaseURL, apiClient, request } from "@/services/api/request";
import { useAssetStore } from "@/stores/use-asset-store";

type UpscaleJob = { status: string; progress: number; error: string };

const TERMINAL_STATUSES = new Set(["completed", "failed"]);

/**
 * 视频超分测试页：本地视频文件或素材库视频 → Gateway /v1/upscale，前端 10s 轮询进度。
 * 独立测试入口，不入资源库、不进任务中心。
 */
export default function ToolsUpscalePage() {
    const [file, setFile] = useState<File | null>(null);
    const [picked, setPicked] = useState<{ resourceId: string; title: string } | null>(null);
    const [pickerOpen, setPickerOpen] = useState(false);
    const [resolution, setResolution] = useState<"1080" | "2160">("1080");
    const [submitting, setSubmitting] = useState(false);
    const [jobId, setJobId] = useState("");
    const [job, setJob] = useState<UpscaleJob | null>(null);
    const assets = useAssetStore((state) => state.assets);

    const pickerItems = useMemo<AssetLibraryPickerItem[]>(
        () =>
            assets
                .filter((asset): asset is Extract<typeof asset, { kind: "video" }> => asset.kind === "video")
                .map((asset) => ({
                    id: asset.id,
                    title: asset.title,
                    category: asset.category || "other",
                    kindLabel: "视频",
                    asset,
                    searchText: (asset.tags || []).join(" "),
                    disabledReason: resourceIdFromStorageKey(asset.data.storageKey) ? undefined : "未上传到云端，无法超分",
                })),
        [assets],
    );

    useEffect(() => {
        if (!jobId || !job || TERMINAL_STATUSES.has(job.status)) return;
        const poll = async () => {
            try {
                const next = await request<UpscaleJob>(apiClient.get(`/tools/upscale/${jobId}`));
                setJob(next);
            } catch (error) {
                message.error(error instanceof Error ? error.message : "查询进度失败");
            }
        };
        const timer = window.setInterval(poll, 10_000);
        return () => window.clearInterval(timer);
    }, [jobId, job]);

    const handleSubmit = async () => {
        if (!file && !picked) return;
        setSubmitting(true);
        try {
            const formData = new FormData();
            if (picked) {
                formData.append("resource_id", picked.resourceId);
            } else if (file) {
                formData.append("video", file);
            }
            formData.append("resolution", resolution);
            const data = await request<{ id: string }>(apiClient.post("/tools/upscale", formData));
            setJobId(data.id);
            setJob({ status: "queued", progress: 0, error: "" });
            message.success("已提交超分任务");
        } catch (error) {
            message.error(error instanceof Error ? error.message : "提交失败");
        } finally {
            setSubmitting(false);
        }
    };

    const completed = job?.status === "completed";
    const failed = job?.status === "failed";
    const contentUrl = jobId ? `${apiBaseURL}/tools/upscale/${jobId}/content` : "";
    const progressPercent = completed ? 100 : Math.min(100, Math.max(0, Math.round(job?.progress ?? 0)));

    return (
        <div className="h-full overflow-y-auto p-8">
            <div className="mx-auto max-w-2xl">
                <Card
                    title={
                        <div className="flex items-center gap-2">
                            <MonitorUp className="size-5" />
                            <span>视频超分</span>
                        </div>
                    }
                >
                    <div className="space-y-4">
                        <div>
                            <label className="mb-2 block text-sm font-medium">视频文件（≤2GB）</label>
                            <input
                                type="file"
                                accept="video/*"
                                className="w-full cursor-pointer rounded-lg border px-3 py-2 text-sm file:mr-3 file:cursor-pointer file:rounded file:border-0 file:bg-black/5 file:px-3 file:py-1"
                                onChange={(event) => {
                                    setFile(event.target.files?.[0] ?? null);
                                    setPicked(null);
                                }}
                            />
                            <div className="mt-2 flex items-center gap-2 text-sm">
                                <Button size="small" icon={<Library className="size-4" />} onClick={() => setPickerOpen(true)}>
                                    从素材库选择
                                </Button>
                                {picked ? (
                                    <span className="flex items-center gap-1 rounded bg-black/5 px-2 py-0.5">
                                        {picked.title || "未命名素材"}
                                        <button type="button" aria-label="取消选择" onClick={() => setPicked(null)}>
                                            <X className="size-3.5" />
                                        </button>
                                    </span>
                                ) : null}
                            </div>
                        </div>
                        <div>
                            <label className="mb-2 block text-sm font-medium">目标分辨率</label>
                            <Radio.Group value={resolution} onChange={(event) => setResolution(event.target.value)}>
                                <Radio.Button value="1080">1080P</Radio.Button>
                                <Radio.Button value="2160">4K（2160P）</Radio.Button>
                            </Radio.Group>
                        </div>
                        <Button type="primary" icon={<MonitorUp className="size-4" />} disabled={(!file && !picked) || submitting} loading={submitting} onClick={handleSubmit}>
                            上传并开始超分
                        </Button>

                        {failed ? <Alert type="error" showIcon message="超分失败" description={job?.error || "未知错误"} /> : null}

                        {jobId && !completed && !failed ? (
                            <div className="space-y-1">
                                <Progress percent={progressPercent} status="active" />
                                <div className="text-xs opacity-60">任务 {jobId} · 状态 {job?.status}（每 10 秒自动刷新）</div>
                            </div>
                        ) : null}

                        {completed ? (
                            <div className="space-y-3">
                                <video controls className="w-full rounded-lg border" src={contentUrl} />
                                <Button icon={<Download className="size-4" />}>
                                    <a href={contentUrl} download={`upscale-${jobId}.mp4`}>
                                        下载成品
                                    </a>
                                </Button>
                            </div>
                        ) : null}

                    </div>
                </Card>
            </div>
            <AssetLibraryPickerModal
                open={pickerOpen}
                items={pickerItems}
                categoryLabels={{ all: "全部视频", other: "其他" }}
                multiple={false}
                title="选择超分视频"
                eyebrow="素材库"
                confirmLabel={() => "使用该素材"}
                emptyTitle="素材库还没有视频"
                emptyDescription="先在创作页生成或上传视频素材。"
                onClose={() => setPickerOpen(false)}
                onConfirm={(ids) => {
                    const item = pickerItems.find((entry) => entry.id === ids[0]);
                    const resourceId = item ? resourceIdFromStorageKey(item.asset?.kind === "video" ? item.asset.data.storageKey : undefined) : "";
                    if (!item || !resourceId) {
                        message.error("该素材没有可用的云端资源");
                        return;
                    }
                    setPicked({ resourceId, title: item.title });
                    setFile(null);
                    setPickerOpen(false);
                }}
            />
        </div>
    );
}
