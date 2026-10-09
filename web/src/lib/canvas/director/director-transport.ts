// 导演台 AI transport：把 director-turn.ts 的消息协议映射到影策既有 /ai/custom 渠道中转。
// 中转是 raw body 透传（含 tools），后端只做登录态 + SSRF 校验；这里映射 OpenAI chat/completions 协议。
import { apiBaseURL } from "@/services/api/request";

import type { DirectorTool } from "./director-agent-tools";
import type { DirectorPlan, DirectorPlanItem, SceneDocument } from "./director-document";
import type { DirectorAssistantMessage, DirectorMessage, DirectorRef, DirectorTransport } from "./director-turn";

export type DirectorChannelConfig = { baseUrl: string; apiKey: string; model: string };

export type DirectorWireTool = { type: "function"; function: { name: string; description: string; parameters: Record<string, unknown> } };

/** 渠道 tools 参数：parameters 已是 z.toJSONSchema 产物，原样映射。 */
export function directorWireTools(tools: readonly DirectorTool[]): DirectorWireTool[] {
    return tools.map((tool) => ({
        type: "function" as const,
        function: { name: tool.name, description: tool.description, parameters: tool.parameters },
    }));
}

type WireContent = string | Array<{ type: "text"; text: string } | { type: "image_url"; image_url: { url: string } }>;

const IMAGE_REF_PATTERN = /\{\{ref (\d+)\}\} \(image\): (\S+)/g;

/** 引擎把 refs 写成 {{ref N}} (kind): content 文本块；图片额外抬成 image_url 多模态块（文本兜底保留）。 */
export function userWireContent(content: string, refs: readonly DirectorRef[] = []): WireContent {
    const imageUrls = refs.filter((ref) => ref.kind === "image" && ref.content).map((ref) => ref.content);
    if (!imageUrls.length) return content;
    return [{ type: "text", text: content }, ...imageUrls.map((url) => ({ type: "image_url" as const, image_url: { url } }))];
}

export function toWireMessages(messages: readonly DirectorMessage[], refs: readonly DirectorRef[] = []): Array<Record<string, unknown>> {
    return messages.map((message) => {
        if (message.role === "assistant") {
            return {
                role: "assistant",
                content: message.content,
                ...(message.toolCalls?.length ? {
                    tool_calls: message.toolCalls.map((call) => ({
                        id: call.id,
                        type: "function",
                        function: { name: call.name, arguments: JSON.stringify(call.args ?? {}) },
                    })),
                } : {}),
            };
        }
        if (message.role === "tool") return { role: "tool", tool_call_id: message.toolCallId, content: message.content };
        if (message.role === "user") return { role: "user", content: userWireContent(message.content, refs) };
        return { role: "system", content: message.content };
    });
}

/** 渠道回复 → 引擎 AssistantMessage。args 必须是合法 JSON（渠道给字符串就先 parse）；失败抛错，引擎整轮报错。 */
export function fromWireResponse(payload: unknown): DirectorAssistantMessage {
    const choice = (payload as { choices?: Array<{ message?: Record<string, unknown> }> })?.choices?.[0];
    const message = choice?.message;
    if (!message) throw new Error("渠道响应缺少 choices[0].message");
    const rawCalls = Array.isArray(message.tool_calls) ? message.tool_calls as Array<{ id?: string; function?: { name?: string; arguments?: string } }> : [];
    return {
        role: "assistant",
        content: typeof message.content === "string" ? message.content : message.content == null ? "" : String(message.content),
        toolCalls: rawCalls.map((call, index) => {
            let args: unknown;
            try {
                args = JSON.parse(call.function?.arguments || "{}");
            } catch {
                throw new Error(`渠道 tool_calls[${index}]（${call.function?.name ?? "?"}）参数不是合法 JSON`);
            }
            return { id: call.id || `call_${index}`, name: call.function?.name || "", args };
        }),
    };
}

/** 影策 /ai/custom 通路：登录态 cookie + X-Canvas-Upstream-* 指向渠道 baseUrl。 */
export function createDirectorTransport(config: DirectorChannelConfig, tools: readonly DirectorWireTool[], refs: readonly DirectorRef[] = []): DirectorTransport {
    return async (messages) => {
        const response = await fetch(`${apiBaseURL.replace(/\/+$/u, "")}/ai/custom`, {
            method: "POST",
            credentials: "include",
            headers: {
                "Content-Type": "application/json",
                Accept: "application/json",
                Authorization: `Bearer ${config.apiKey || "director"}`,
                "X-Canvas-Upstream-URL": `${config.baseUrl.replace(/\/+$/u, "")}/chat/completions`,
                "X-Canvas-Upstream-Format": "openai",
            },
            body: JSON.stringify({ model: config.model, messages: toWireMessages(messages, refs), tools, stream: false }),
        });
        if (!response.ok) throw new Error(`渠道请求失败：HTTP ${response.status} ${(await response.text()).slice(0, 200)}`);
        return fromWireResponse(await response.json());
    };
}
