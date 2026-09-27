import type { agentproto } from '@wash/ui';

export function applyWorkspacePatch(current: agentproto.WorkspaceState, patch: agentproto.WorkspacePatch): agentproto.WorkspaceState | null {
  if (!current.workspace || current.sequence !== patch.base) return null;
  const workspace = { ...current.workspace, ...patch.workspace };
  if (patch.plan) {
    const nodes = new Map((workspace.plan ?? []).map((node) => [node.id, node]));
    for (const id of patch.plan.remove ?? []) nodes.delete(id);
    for (const node of patch.plan.upsert ?? []) nodes.set(node.id, node);
    const order = patch.plan.order ?? [...nodes.keys()];
    if (order.length !== nodes.size || new Set(order).size !== nodes.size || order.some((id) => !nodes.has(id))) return null;
    workspace.plan = order.map((id) => nodes.get(id)!);
  }
  return { ...current, ...patch.frame, workspace, sequence: patch.sequence };
}
