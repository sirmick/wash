import type { agentproto } from '@wash/ui';

export function applyWorkspacePatch(current: agentproto.WorkspaceState, patch: agentproto.WorkspacePatch): agentproto.WorkspaceState | null {
  if (!current.workspace || current.sequence !== patch.base) return null;
  const workspace = { ...current.workspace, ...patch.workspace };
  if (patch.items) {
    const items = new Map((workspace.items ?? []).map((item) => [item.id, item]));
    for (const id of patch.items.remove ?? []) items.delete(id);
    for (const item of patch.items.upsert ?? []) items.set(item.id, item);
    const order = patch.items.order ?? [...items.keys()];
    if (order.length !== items.size || new Set(order).size !== items.size || order.some((id) => !items.has(id))) return null;
    workspace.items = order.map((id) => items.get(id)!);
  }
  return { ...current, ...patch.frame, workspace, sequence: patch.sequence };
}
