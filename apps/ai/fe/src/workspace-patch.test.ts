import { test } from 'node:test';
import assert from 'node:assert/strict';
import { applyWorkspacePatch } from './workspace-patch.ts';
import type { WorkspaceFrame } from './WorkspaceSidebar';

test('keyed plan deltas preserve untouched items and reject a missed base', () => {
 const first={id:'a',text:'First',state:'pending',revision:1};
 const second={id:'b',text:'Second',state:'pending',revision:1};
 const frame={sequence:4,workspace:{id:'w',items:[first,second]}} as WorkspaceFrame;
 const patch={base:4,sequence:5,frame:{},workspace:{revision:2},items:{upsert:[{...first,state:'done'}],remove:[]}};
 const next=applyWorkspacePatch(frame,patch)!;
 assert.equal(next.workspace!.items[1],second);
 assert.equal(next.workspace!.items[0].state,'done');
 assert.equal(frame.workspace!.items[0].state,'pending');
 assert.equal(applyWorkspacePatch(next,patch),null);
 assert.equal(applyWorkspacePatch(frame,{...patch,items:{upsert:[],remove:[],order:['a','a']}}),null);
 const reordered=applyWorkspacePatch(next,{base:5,sequence:6,frame:{},workspace:{},items:{upsert:[],remove:['a'],order:['b']}})!;
 assert.deepEqual(reordered.workspace!.items,[second]);
});
