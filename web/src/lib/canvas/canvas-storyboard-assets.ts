import { CanvasNodeType, type CanvasNodeData, type StoryboardAssetBinding, type StoryboardAssetRole } from "@/types/canvas";

export type StoryboardAssetCatalogItem = {
    id: string;
    title: string;
    type: "image" | "video" | "audio" | "character";
    category?: string;
    tags: string[];
    prompt: string;
    characterAssetId?: string;
    characterVersionId?: string;
};

const OUTPUT_WORKFLOW_KINDS = new Set(["shot", "action_board", "final"]);
const STORYBOARD_ASSET_ROLES = new Set<StoryboardAssetRole>(["character", "environment", "wardrobe", "prop", "weapon", "style", "motion", "audio"]);

export function buildStoryboardAssetCatalog(nodes: CanvasNodeData[]): StoryboardAssetCatalogItem[] {
    return nodes.flatMap((node): StoryboardAssetCatalogItem[] => {
        const type = storyboardAssetType(node);
        if (!type || OUTPUT_WORKFLOW_KINDS.has(node.metadata?.workflowKind || "")) return [];
        if (!node.metadata?.content && !node.metadata?.storageKey && !node.metadata?.assetId && type !== "character") return [];
        const prompt = compactStoryboardAssetText(node.metadata?.prompt || node.metadata?.workflowDescription || node.metadata?.characterPrompt || "");
        return [{
            id: node.id,
            title: compactStoryboardAssetText(node.title, 120) || "未命名资产",
            type,
            category: node.metadata?.assetCategory,
            tags: Array.from(new Set((node.metadata?.assetTags || []).map((tag) => compactStoryboardAssetText(tag, 64)).filter(Boolean))).slice(0, 12),
            prompt,
            characterAssetId: node.metadata?.characterAssetId,
            characterVersionId: node.metadata?.characterVersionId,
        }];
    }).slice(0, 60);
}

export function storyboardAssetRoleForNode(node: CanvasNodeData): StoryboardAssetRole | null {
    if (node.metadata?.workflowKind === "character" || node.metadata?.assetCategory === "character") return "character";
    if (node.type === CanvasNodeType.Audio) return "audio";
    if (node.type === CanvasNodeType.Video) return "motion";
    const category = node.metadata?.assetCategory;
    if (category === "environment" || category === "prop") return category;
    if (node.type === CanvasNodeType.Image || node.type === CanvasNodeType.Drawing) return "style";
    return null;
}

export function normalizeStoryboardAssetBindings(bindings: StoryboardAssetBinding[] | undefined, nodes?: CanvasNodeData[]) {
    const nodeIds = nodes ? new Set(nodes.map((node) => node.id)) : null;
    const seen = new Set<string>();
    return (bindings || []).flatMap((binding): StoryboardAssetBinding[] => {
        const nodeId = String(binding?.nodeId || "").trim();
        if (!nodeId || seen.has(nodeId) || !STORYBOARD_ASSET_ROLES.has(binding.role) || (nodeIds && !nodeIds.has(nodeId))) return [];
        seen.add(nodeId);
        return [{ nodeId, role: binding.role, priority: Math.max(0, Math.min(100, Math.round(Number(binding.priority) || 0))) }];
    }).sort((left, right) => right.priority - left.priority);
}

function storyboardAssetType(node: CanvasNodeData): StoryboardAssetCatalogItem["type"] | null {
    if (node.metadata?.workflowKind === "character" && node.metadata.characterAssetId && node.metadata.characterVersionId) return "character";
    if (node.type === CanvasNodeType.Image || node.type === CanvasNodeType.Drawing) return "image";
    if (node.type === CanvasNodeType.Video) return "video";
    if (node.type === CanvasNodeType.Audio) return "audio";
    return null;
}

function compactStoryboardAssetText(value: string, limit = 600) {
    const normalized = value.replace(/\s+/g, " ").trim();
    return normalized.length > limit ? `${normalized.slice(0, limit)}…` : normalized;
}

// 生成完成后按角色名/正文提及自动绑定画布资产：
// characters（字符串或 {characterName}）匹配图片节点标题 → character；
// 正文提及节点标题（≥2 字）→ environment。手动拖拽绑定优先，已绑定节点不覆盖。
export function autoBindStoryboardRowAssets<T extends { assetBindings?: StoryboardAssetBinding[] }>(
    rows: T[],
    nodes: CanvasNodeData[],
): T[] {
    const candidates = nodes
        .filter((node) => node.type === CanvasNodeType.Image || node.type === CanvasNodeType.Drawing)
        .map((node) => ({ node, title: (node.title || "").trim().toLowerCase() }))
        .filter((item) => item.title.length >= 1);
    if (!candidates.length) return rows;
    const characterName = (value: unknown): string => {
        if (typeof value === "string") return value.trim().toLowerCase();
        if (value && typeof value === "object") {
            const name = (value as { characterName?: unknown }).characterName;
            if (typeof name === "string") return name.trim().toLowerCase();
        }
        return "";
    };
    return rows.map((row) => {
        const bindings: StoryboardAssetBinding[] = [...(row.assetBindings || [])];
        const push = (nodeId: string, role: StoryboardAssetRole, priority: number) => {
            if (bindings.every((binding) => binding.nodeId !== nodeId)) bindings.push({ nodeId, role, priority });
        };
        const record = row as { characters?: unknown; plotDescription?: unknown; videoMotionPrompt?: unknown };
        const names = (Array.isArray(record.characters) ? record.characters : [])
            .map(characterName)
            .filter(Boolean);
        const text = `${String(record.plotDescription || "")} ${String(record.videoMotionPrompt || "")}`.toLowerCase();
        for (const { node, title } of candidates) {
            // 短标题（如 A/B）只做精确匹配，避免单字符子串误伤；≥2 字才允许包含匹配与正文提及匹配。
            if (names.some((name) => name === title || (title.length >= 2 && name.includes(title)))) push(node.id, "character", 60);
            else if (title.length >= 2 && text.includes(title)) push(node.id, "environment", 40);
        }
        return bindings.length ? { ...row, assetBindings: bindings } : row;
    });
}
