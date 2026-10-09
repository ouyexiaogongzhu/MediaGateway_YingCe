import assert from "node:assert/strict";
import test from "node:test";

import { fromWireResponse, toWireMessages, userWireContent } from "./director-transport";
import type { DirectorRef } from "./director-turn";

test("toWireMessages 映射四种角色，assistant toolCalls 序列化为 arguments 字符串", () => {
    const wire = toWireMessages([
        { role: "system", content: "sys" },
        { role: "user", content: "指令" },
        { role: "assistant", content: "", toolCalls: [{ id: "c1", name: "editDocument", args: { operations: [{ op: "replace" }] } }] },
        { role: "tool", toolCallId: "c1", name: "editDocument", content: '{"applied":1}' },
        { role: "assistant", content: "完成" },
    ]);
    assert.deepEqual(wire[0], { role: "system", content: "sys" });
    const calls = (wire[2]!.tool_calls as Array<{ type: string; function: { name: string; arguments: string } }>);
    assert.equal(calls[0]!.function.arguments, '{"operations":[{"op":"replace"}]}');
    assert.equal(calls[0]!.type, "function");
    assert.deepEqual(wire[3], { role: "tool", tool_call_id: "c1", content: '{"applied":1}' });
    assert.deepEqual(wire[4], { role: "assistant", content: "完成" });
    assert.equal("tool_calls" in wire[4]!, false);
});

test("图片 refs 抬成 image_url 多模态块，无图片保持纯文本", () => {
    const refs: DirectorRef[] = [
        { kind: "image", content: "http://x/a.png" },
        { kind: "text", content: "描述" },
    ];
    const withImage = userWireContent("指令\n\n{{ref 1}} (image): http://x/a.png", refs);
    assert.deepEqual(withImage, [
        { type: "text", text: "指令\n\n{{ref 1}} (image): http://x/a.png" },
        { type: "image_url", image_url: { url: "http://x/a.png" } },
    ]);
    assert.equal(userWireContent("纯文本", []), "纯文本");
});

test("fromWireResponse 解析 tool_calls，arguments 非法 JSON 抛错", () => {
    const ok = fromWireResponse({
        choices: [{ message: { role: "assistant", content: null, tool_calls: [{ id: "c9", function: { name: "readDocument", arguments: '{"section":"scene"}' } }] } }],
    });
    assert.deepEqual(ok.toolCalls, [{ id: "c9", name: "readDocument", args: { section: "scene" } }]);
    assert.equal(ok.content, "");

    assert.throws(() => fromWireResponse({
        choices: [{ message: { tool_calls: [{ id: "c1", function: { name: "editDocument", arguments: "{broken" } }] } }],
    }), /不是合法 JSON/);

    assert.throws(() => fromWireResponse({ error: "no choices" }), /choices/);
});
