import { nanoid } from "nanoid";
import { useCallback, useState } from "react";
import { Button } from "antd";
import { Bot, Play, Square } from "lucide-react";

import { createDirectorDraft } from "@/lib/canvas/director/director-agent-tools";
import { documentFromScene, type DirectorPlan, type DirectorPlanItem, type SceneDocument } from "@/lib/canvas/director/director-document";
import { createDirectorTransport, directorWireTools, type DirectorChannelConfig } from "@/lib/canvas/director/director-transport";
import { runDirectorTurn, type DirectorRef } from "@/lib/canvas/director/director-turn";
import type { CanvasNodeData } from "@/types/canvas";
import type { DirectorScene } from "@/types/director";

/** AI 导演面板：指令 → runDirectorTurn → 合法方案进列表 + 草稿场景经 onDraft 落回工作台。 */
export function DirectorAiPanel({ scene, imageNodes, plans, onPlansChange, onDraft, onPreview, activePlanId }: { scene: DirectorScene; imageNodes: CanvasNodeData[]; plans: DirectorPlanItem[]; onPlansChange: (plans: DirectorPlanItem[]) => void; onDraft: (draft: { scene: SceneDocument; plan: DirectorPlan }) => void; onPreview: (plan: DirectorPlan, id: string) => void; activePlanId: string | null }) {
    const [instruction, setInstruction] = useState("");
    const [config, setConfig] = useState<DirectorChannelConfig>({ baseUrl: "http://127.0.0.1:8600/v1", apiKey: "director", model: "grok-chat-fast" });
    const [running, setRunning] = useState(false);
    const [status, setStatus] = useState<{ tone: "ok" | "warn" | "error"; text: string } | null>(null);
    const [controller, setController] = useState<AbortController | null>(null);

    const run = useCallback(async () => {
        if (running || !instruction.trim()) return;
        setRunning(true);
        setStatus(null);
        const abort = new AbortController();
        setController(abort);
        try {
            const document = documentFromScene(scene);
            // 工具 schema 与引擎共用一份定义；draft 只为取 name/description/parameters 上报渠道。
            const draft = createDirectorDraft(document, undefined, plans);
            const refs: DirectorRef[] = imageNodes.slice(0, 8)
                .map((node) => ({ kind: "image" as const, content: String(node.metadata?.content ?? "") }))
                .filter((ref) => ref.content);
            const result = await runDirectorTurn({
                scene: document,
                plans,
                instruction,
                transport: createDirectorTransport(config, directorWireTools(draft.tools), refs),
                signal: abort.signal,
            });
            if (result.status === "aborted") {
                setStatus({ tone: "warn", text: "已停止" });
            } else if (result.valid && result.draft) {
                onDraft(result.draft);
                const id = nanoid();
                onPlansChange([...plans, { id, instruction, ...result.draft.plan }]);
                onPreview(result.draft.plan, id);
                setStatus({ tone: "ok", text: `${result.message.slice(0, 80) || "完成"}（${result.rounds} 轮工具）` });
            } else if (result.status === "completed" || result.status === "circuit-breaker") {
                setStatus({ tone: "warn", text: `${result.status === "circuit-breaker" ? "达到熔断上限" : "方案未通过校验"}：${(result.issues[0] ?? result.message).slice(0, 120)}` });
            } else {
                setStatus({ tone: "error", text: `${result.status}: ${(result.issues[0] ?? result.message).slice(0, 120)}` });
            }
        } catch (error) {
            setStatus({ tone: "error", text: error instanceof Error ? error.message.slice(0, 160) : String(error) });
        } finally {
            setRunning(false);
            setController(null);
        }
    }, [running, instruction, scene, plans, imageNodes, config, onDraft, onPlansChange, onPreview]);

    return (
        <div className="border-b px-2 pb-3" style={{ borderColor: "var(--border)" }}>
            <div className="flex items-center gap-1.5 py-2 text-xs font-medium"><Bot className="size-3.5" />AI 导演</div>
            <textarea
                data-testid="director-ai-instruction"
                className="mb-1.5 h-16 w-full resize-none rounded border p-1.5 text-xs"
                style={{ borderColor: "var(--border)", background: "transparent", color: "inherit" }}
                placeholder="例：女人偶坐在床边，镜头从门口缓推到中景"
                value={instruction}
                onChange={(event) => setInstruction(event.target.value)}
            />
            <div className="mb-1.5 flex gap-1">
                <input aria-label="渠道 Base URL" className="min-w-0 flex-1 rounded border px-1.5 py-1 font-mono text-[10px]" style={{ borderColor: "var(--border)", background: "transparent", color: "inherit" }} value={config.baseUrl} onChange={(event) => setConfig({ ...config, baseUrl: event.target.value })} />
                <input aria-label="模型" className="w-24 rounded border px-1.5 py-1 font-mono text-[10px]" style={{ borderColor: "var(--border)", background: "transparent", color: "inherit" }} value={config.model} onChange={(event) => setConfig({ ...config, model: event.target.value })} />
            </div>
            {running ? (
                <Button block size="small" danger icon={<Square className="size-3" />} onClick={() => controller?.abort()}>停止</Button>
            ) : (
                <Button block size="small" type="primary" icon={<Play className="size-3" />} data-testid="director-ai-run" disabled={!instruction.trim()} onClick={() => void run()}>生成方案</Button>
            )}
            {status ? <div className={`mt-1.5 text-[10px] leading-snug ${status.tone === "error" ? "text-red-500" : status.tone === "warn" ? "text-amber-500" : "text-emerald-500"}`} data-testid="director-ai-status">{status.text}</div> : null}
            {plans.length ? (
                <div className="mt-2">
                    <div className="py-1 text-[10px] opacity-60">方案列表（点选预演）</div>
                    {plans.map((plan) => (
                        <button
                            key={plan.id}
                            type="button"
                            data-testid="director-ai-plan"
                            className={`mb-1 block w-full truncate rounded border px-1.5 py-1 text-left text-[11px] ${activePlanId === plan.id ? "border-sky-500 text-sky-400" : "text-current"}`}
                            style={{ borderColor: activePlanId === plan.id ? undefined : "var(--border)" }}
                            onClick={() => onPreview(plan, plan.id)}
                        >
                            {plan.name} · {plan.duration}s · {plan.tracks.length} 轨
                        </button>
                    ))}
                </div>
            ) : null}
        </div>
    );
}
