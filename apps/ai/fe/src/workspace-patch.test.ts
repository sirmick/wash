import type { agentproto } from '@wash/ui';
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { applyWorkspacePatch } from './workspace-patch.ts';

test('plan node deltas preserve untouched nodes and reject a missed base', () => {
 const first={id:'a',title:'First',state:'todo',revision:1};
 const second={id:'b',title:'Second',state:'todo',revision:1};
 const frame={sequence:4,workspace:{id:'w',plan:[first,second]}} as agentproto.WorkspaceState;
 const patch:agentproto.WorkspacePatch={kind:'workspace_patch',key:'k',base:4,sequence:5,frame:{},workspace:{revision:2},plan:{upsert:[{...first,state:'done'}],remove:[]}};
 const next=applyWorkspacePatch(frame,patch)!;
 assert.equal(next.workspace!.plan![1],second);
 assert.equal(next.workspace!.plan![0].state,'done');
 assert.equal(frame.workspace!.plan![0].state,'todo');
 assert.equal(applyWorkspacePatch(next,patch),null);
 assert.equal(applyWorkspacePatch(frame,{...patch,plan:{upsert:[],remove:[],order:['a','a']}}),null);
 const reordered=applyWorkspacePatch(next,{kind:'workspace_patch',key:'k',base:5,sequence:6,frame:{},workspace:{},plan:{upsert:[],remove:['a'],order:['b']}})!;
 assert.deepEqual(reordered.workspace!.plan,[second]);
});
