import { expect, test } from "bun:test";
import { autoBindStoryboardRowAssets } from "@/lib/canvas/canvas-storyboard-assets";
import { CanvasNodeType, type CanvasNodeData } from "@/types/canvas";

const snap = JSON.parse(await Bun.file("/tmp/snap3.json").text());
const scriptText = JSON.parse(await Bun.file("/tmp/scripttext.json").text()).content;
const rawRows = JSON.parse(await Bun.file("/tmp/rows.json").text());

test("真實快照：走完整鏈路後還有沒有懸空綁定", () => {
    const nodes: CanvasNodeData[] = snap.nodes;
    const ids = new Set(nodes.map(n => n.id));
    const imgNodes = nodes.filter(n => n.type === "image");
    // 依 script 節點上游順序重建 mentionRefs（@图片N → 節點）
    const mentionRefs = imgNodes.map((n, i) => ({ kind: "image", nodeId: n.id, label: `@图片${i + 1}` }));
    const out = autoBindStoryboardRowAssets(rawRows as never[], nodes, { scriptText, mentionRefs: mentionRefs as never });
    let dangling = 0, total = 0;
    for (const r of out) {
        for (const b of (r.assetBindings ?? [])) { total++; if (!ids.has(b.nodeId)) dangling++; }
    }
    console.log("總綁定", total, "懸空", dangling);
    console.log("row2 →", JSON.stringify(out[1]?.assetBindings?.map(b => `${b.role}:${(nodes.find(n=>n.id===b.nodeId)?.title)||"?"}`)));
    expect(dangling).toBe(0);
    expect(total).toBeGreaterThan(0);
});
