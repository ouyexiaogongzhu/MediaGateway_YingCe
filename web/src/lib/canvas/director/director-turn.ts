// 框架无关的导演台 AI 一轮循环引擎：transport 协议 + 工具分发 + 纠错循环 + 熔断/超时/abort。
// 集成者只需提供 transport：把 messages 映射到真实 chat 通道，把模型回复的 tool_calls 填进 DirectorAssistantMessage。
import { directorPrompt } from "./director-agent-prompt";
import { createDirectorDraft } from "./director-agent-tools";
import type { DirectorPlan, DirectorPlanItem, SceneDocument } from "./director-document";

export type DirectorToolCall = { id: string; name: string; args: unknown };

export type DirectorSystemMessage = { role: "system"; content: string };
export type DirectorUserMessage = { role: "user"; content: string };
export type DirectorAssistantMessage = { role: "assistant"; content: string; toolCalls?: DirectorToolCall[] };
export type DirectorToolResultMessage = { role: "tool"; toolCallId: string; name: string; content: string };
export type DirectorMessage = DirectorSystemMessage | DirectorUserMessage | DirectorAssistantMessage | DirectorToolResultMessage;

/** 集成者把模型单轮回复包成 AssistantMessage；toolCalls 依原样回填（id/name/args）。 */
export type DirectorTransport = (messages: readonly DirectorMessage[]) => Promise<DirectorAssistantMessage>;

/** {{ref N}} 素材：引擎只负责在 user 消息里给出编号文本块；图片/视频由集成者在渠道层替换为多模态块。 */
export type DirectorRef = { kind: "text" | "image" | "video"; content: string };

export type DirectorTurnOptions = {
    scene: SceneDocument;
    plan?: DirectorPlan;
    plans?: DirectorPlanItem[];
    instruction: string;
    refs?: DirectorRef[];
    transport: DirectorTransport;
    signal?: AbortSignal;
    /** 默认 10 分钟；测试可调小。 */
    timeoutMs?: number;
};

export type DirectorTurnResult = {
    status: "completed" | "circuit-breaker" | "timeout" | "aborted" | "error";
    /** 模型最后一条文本。 */
    message: string;
    /** 已执行的工具轮数。 */
    rounds: number;
    valid: boolean;
    issues: string[];
    /** 仅在 valid=true 时提供完整草稿。 */
    draft?: { scene: SceneDocument; plan: DirectorPlan };
};

const MAX_TOOL_ROUNDS = 40;
const DEFAULT_TIMEOUT_MS = 10 * 60 * 1000;

export async function runDirectorTurn(options: DirectorTurnOptions): Promise<DirectorTurnResult> {
    try {
        return await loop(options);
    } catch (error) {
        // 初始 scene/plan 非法（createDirectorDraft 抛出）或未预期异常：整轮失败。
        return {
            status: "error",
            message: error instanceof Error ? error.message : String(error),
            rounds: 0,
            valid: false,
            issues: [],
        };
    }
}

async function loop(options: DirectorTurnOptions): Promise<DirectorTurnResult> {
    const { scene, plan, plans, instruction, transport } = options;
    const timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS;
    const timeoutSignal = AbortSignal.timeout(timeoutMs);
    const signal = options.signal ? AbortSignal.any([options.signal, timeoutSignal]) : timeoutSignal;
    const abortStatus = () => timeoutSignal.aborted ? ("timeout" as const) : ("aborted" as const);

    const draft = createDirectorDraft(scene, plan, plans);
    const toolsByName = new Map(draft.tools.map(tool => [tool.name, tool]));

    const refBlock = (options.refs ?? []).map((ref, index) => `{{ref ${index + 1}}} (${ref.kind}): ${ref.content}`).join("\n");
    const messages: DirectorMessage[] = [
        { role: "system", content: directorPrompt },
        { role: "user", content: refBlock ? `${instruction}\n\n参考素材：\n${refBlock}` : instruction },
    ];

    let rounds = 0;
    let message = "";
    while (true) {
        if (signal.aborted) return finish(abortStatus());
        if (rounds >= MAX_TOOL_ROUNDS) return finish("circuit-breaker");
        const assistant = await transport(messages);
        // 传输期间可能已超时/中止：不能把迟到的回复当成功。
        if (signal.aborted) return finish(abortStatus());
        message = assistant.content ?? "";
        messages.push(assistant);
        const calls = assistant.toolCalls ?? [];
        if (!calls.length) return finish("completed");
        rounds++;
        for (const call of calls) {
            messages.push(executeTool(call));
            if (signal.aborted) return finish(abortStatus());
        }
    }

    function executeTool(call: DirectorToolCall): DirectorToolResultMessage {
        const base = { role: "tool" as const, toolCallId: call.id, name: call.name };
        const tool = toolsByName.get(call.name);
        if (!tool) return { ...base, content: `未知工具：${call.name}，只允许 readDocument / editDocument` };
        try {
            return { ...base, content: JSON.stringify(tool.execute(call.args, signal)) };
        } catch (error) {
            // abort 是流程级信号，不伪装成校验错误；外层紧接的 aborted 检查会结束循环。
            if (signal.aborted) return { ...base, content: "本轮已中止" };
            return { ...base, content: error instanceof Error ? error.message : String(error) };
        }
    }

    function finish(status: DirectorTurnResult["status"]): DirectorTurnResult {
        const summary = draft.summary();
        return {
            status,
            message,
            rounds,
            valid: summary.valid,
            issues: summary.issues,
            ...(summary.valid ? { draft: draft.read() } : {}),
        };
    }
}
