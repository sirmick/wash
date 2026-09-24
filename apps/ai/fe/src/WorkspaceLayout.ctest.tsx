import { afterEach, expect, test, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library';
import { createSignal } from 'solid-js';
import type { WorkspaceFrame } from './WorkspaceSidebar';

import { WorkspaceLayout } from './WorkspaceLayout';

// jsdom has no scrolling implementation; the transcript uses this browser API.
HTMLElement.prototype.scrollTo = vi.fn();
afterEach(cleanup);
const frame = (): WorkspaceFrame => ({workspace: {
 id:'w', name:'Timer team', state:'active',revision:1,orchestrator:'lead',
 items:[{id:'timer',text:'Build timer',state:'active',revision:1}],
 members:[{id:'lead',name:'Architect',session_id:'s',provider:'codex',lifetime:'resident',state:'available',emoji:'🔎',status:'Reviewing'}],
 assignments:[],messages:[],document:{path:'/project/PLAN.md',title:'Design notes'},
},activity:{lead:'working'},document_text:'# Timer design\nA monotonic clock.'});

test('document and member inspection preserve the conversation and send distinct human actions', async () => {
 const onAction=vi.fn(); render(() => <WorkspaceLayout frame={frame()} onAction={onAction}>Conversation</WorkspaceLayout>);
 await fireEvent.click(screen.getByTestId('workspace-plan-link'));
 expect(screen.getByTestId('workspace-item-timer').textContent).toContain('active');
 await fireEvent.click(screen.getByRole('button', {name:'Design notes'}));
 expect(screen.getByTestId('workspace-document').textContent).toContain('monotonic');
 await fireEvent.click(screen.getByTestId('workspace-member-lead'));
 expect(onAction).toHaveBeenCalledWith('member_inspect',{member_id:'lead'});
 expect(onAction).not.toHaveBeenCalledWith('member_open',expect.anything());
 const input=screen.getByLabelText('Message Architect');
 await fireEvent.input(input,{target:{value:'Check the clock'}});
 await fireEvent.click(screen.getByText('Send message'));
 expect(onAction).toHaveBeenCalledWith('member_message',{recipient:'lead',body:'Check the clock'});
});

test('plan and document update live while selection and a human draft survive', async () => {
 const [value,setValue]=createSignal(frame());
 render(() => <WorkspaceLayout frame={value()} onAction={()=>{}}>Conversation</WorkspaceLayout>);
 await fireEvent.click(screen.getByTestId('workspace-member-lead'));
 await fireEvent.input(screen.getByLabelText('Message Architect'),{target:{value:'Unsaved feedback'}});
 setValue({...value(),workspace:{...value().workspace!,revision:2,items:[{id:'timer',text:'Build timer',state:'done',revision:2}]}});

 expect((screen.getByLabelText('Message Architect') as HTMLTextAreaElement).value).toBe('Unsaved feedback');
 await fireEvent.click(screen.getByRole('button', {name:'Design notes'}));
 setValue({...value(),document_text:'# New design\nClock verified.'});
 expect(screen.getByTestId('workspace-document').textContent).toContain('Clock verified');
 expect(screen.getByTestId('workspace-item-timer').textContent).toContain('done');
 await fireEvent.click(screen.getByRole('tab', {name:/Architect/}));
 expect((screen.getByLabelText('Message Architect') as HTMLTextAreaElement).value).toBe('Unsaved feedback');
});

test('decisions and paused member recovery have explicit controls', async () => {
 const f=frame();f.workspace!.members[0].state='paused';f.workspace!.messages=[{id:'q',sender:'lead',recipient:'human',type:'decision_request',body:'Ship?',delivery:'recorded'}];
 const onAction=vi.fn();render(()=> <WorkspaceLayout frame={f} onAction={onAction}>Conversation</WorkspaceLayout>);
 await fireEvent.input(screen.getByLabelText('Decision response'),{target:{value:'Proceed'}});
 await fireEvent.click(screen.getByText('Answer'));
 expect(onAction).toHaveBeenCalledWith('decision_response',{id:'q',body:'Proceed'});
 await fireEvent.click(screen.getByTestId('workspace-member-lead'));
 await fireEvent.click(screen.getByText('Resume member'));
 expect(onAction).toHaveBeenCalledWith('member_resume',{member_id:'lead'});
});


test('live activity and usage update independently of the plan and retain unknown counts', async () => {
 const [value,setValue]=createSignal(frame());
 render(() => <WorkspaceLayout frame={value()} onAction={()=>{}}>Conversation</WorkspaceLayout>);
 const dot=screen.getByTestId('workspace-activity-lead');
 expect(screen.getByTestId('workspace-usage-lead').textContent).toContain('not reported');
 setValue({...value(),activity:{lead:'thinking'},usage:{lead:{used:14689,size:258400}}});
 expect(dot.getAttribute('data-activity')).toBe('thinking');
 expect(dot.getAttribute('data-pulse')).toBe('true');
 expect(screen.getByTestId('workspace-usage-lead').textContent).toContain('14,689 / 258,400 tokens');
 setValue({...value(),activity:{lead:'tool'},activity_detail:{lead:'Running tests'}});
 expect(screen.getByText('Running tests')).toBeTruthy();
 expect(screen.getByTestId('workspace-activity-label-lead').textContent).toBe('Using tools');
 setValue({...value(),activity:{lead:'waiting-message'}});
 expect(dot.getAttribute('data-pulse')).toBe('false');
 expect(screen.queryByText('Running tests')).toBeNull();
 expect(screen.getByTestId('workspace-activity-label-lead').textContent).toBe('Awaiting message');
});

test('tabs deduplicate, keep conversation mounted and reset only when the workspace changes', async () => {
 const [value,setValue]=createSignal<WorkspaceFrame>({workspace:null});
 const onAction=vi.fn();
 render(() => <WorkspaceLayout frame={value()} currentSessionID="own" onAction={onAction}><textarea aria-label="Conversation draft"/></WorkspaceLayout>);
 const input=screen.getByLabelText('Conversation draft') as HTMLTextAreaElement;
 await fireEvent.input(input,{target:{value:'Keep my work'}});
 expect(screen.queryByRole('tablist')).toBeNull();
 const f=frame();f.workspace!.members.push({id:'owner',name:'Orchestrator',session_id:'own',provider:'codex',lifetime:'resident',state:'available'});
 setValue(f);
 expect(onAction).toHaveBeenLastCalledWith('member_inspect',{member_id:''});
 await fireEvent.click(screen.getByTestId('workspace-member-lead'));
 await fireEvent.click(screen.getByTestId('workspace-member-lead'));
 expect(screen.getAllByRole('tab')).toHaveLength(2);
 expect(screen.getByTestId('workspace-sidebar').querySelector('[data-testid="workspace-member-detail"]')).toBeNull();
 expect(screen.getByTestId('workspace-main').contains(screen.getByTestId('workspace-member-detail'))).toBe(true);
 expect(screen.queryByTestId('agent-composer')).toBeNull();
 await fireEvent.input(screen.getByLabelText('Message Architect'),{target:{value:'Member draft'}});
 await fireEvent.click(screen.getByTestId('workspace-plan-link'));
 expect(onAction).toHaveBeenLastCalledWith('member_inspect',{member_id:''});
 expect(screen.getByTestId('workspace-sidebar').querySelector('[data-testid="workspace-plan"]')).toBeNull();
 await fireEvent.keyDown(screen.getByRole('tab',{name:/Plan/}),{key:'ArrowLeft'});
 expect(screen.getByRole('tab',{name:/Architect/}).getAttribute('aria-selected')).toBe('true');
 expect((screen.getByLabelText('Message Architect') as HTMLTextAreaElement).value).toBe('Member draft');
 await fireEvent.click(screen.getByTestId('workspace-member-owner'));
 expect(screen.getByRole('tab',{name:'Conversation'}).getAttribute('aria-selected')).toBe('true');
 expect(screen.getByLabelText('Conversation draft')).toBe(input);
 expect(input.value).toBe('Keep my work');
 await fireEvent.click(screen.getByTestId('workspace-close-lead'));
 expect(screen.queryByRole('tab',{name:/Architect/})).toBeNull();
 await fireEvent.click(screen.getByTestId('workspace-member-lead'));
 setValue({workspace:null});
 expect(screen.queryByRole('tablist')).toBeNull();
 expect(screen.queryByTestId('workspace-sidebar')).toBeNull();
 expect(screen.getByLabelText('Conversation draft')).toBe(input);
 expect(input.value).toBe('Keep my work');
 setValue({...f,workspace:{...f.workspace!,id:'another'}});
 expect(screen.getAllByRole('tab')).toHaveLength(1);
 await fireEvent.click(screen.getByTestId('workspace-member-lead'));
 expect((screen.getByLabelText('Message Architect') as HTMLTextAreaElement).value).toBe('');
 setValue({...value(),workspace:{...value().workspace!,members:[]}});
 expect(screen.getByRole('tab',{name:'Conversation'}).getAttribute('aria-selected')).toBe('true');
});

test('sidebar width is keyboard resizable, bounded and remembered', async () => {
 localStorage.removeItem('wash.agent.workspace.split');
 const mounted=render(() => <WorkspaceLayout frame={frame()} onAction={()=>{}}>Conversation</WorkspaceLayout>);
 const divider=screen.getByRole('separator');
 await fireEvent.keyDown(divider,{key:'ArrowLeft'});
 expect(divider.getAttribute('aria-valuenow')).toBe('68');
 await fireEvent.keyDown(divider,{key:'Home'});
 await fireEvent.keyDown(divider,{key:'ArrowLeft'});
 expect(divider.getAttribute('aria-valuenow')).toBe('35');
 await fireEvent.keyDown(divider,{key:'End'});
 expect(divider.getAttribute('aria-valuenow')).toBe('85');
 mounted.unmount();
 render(() => <WorkspaceLayout frame={frame()} onAction={()=>{}}>Conversation</WorkspaceLayout>);
 expect(screen.getByRole('separator').getAttribute('aria-valuenow')).toBe('85');
 localStorage.removeItem('wash.agent.workspace.split');
});

test('QA opens in the main panel, refreshes live and approval controls address the selected teammate', async () => {
 const f=frame();f.workspace!.qa=[{id:'q1',package:'K5',title:'Wakeup bound',assignee:'lead',state:'open',blocking:true,revision:1}];f.qa_markdown='# Workspace QA\n\n## K5 · q1 — Wakeup bound\n\nAwaiting architect.';
 f.workspace!.qa_document={path:'/data/project/QA.md',title:'Project QA'};
 f.preview={member_id:'lead',events:[],asks:[{id:'approval-1',tool:'Read',subject:'workspace_get',age_ms:0}]};
 const [value,setValue]=createSignal(f);const onAction=vi.fn(),onAnswer=vi.fn();
 render(() => <WorkspaceLayout frame={value()} onAction={onAction} onAnswer={onAnswer}>Conversation</WorkspaceLayout>);
 await fireEvent.click(screen.getByTestId('workspace-question-q1'));
 expect(screen.getByRole('tab',{name:/Questions/}).getAttribute('aria-selected')).toBe('true');
 expect(screen.getByTestId('workspace-qa').textContent).toContain('Awaiting architect');
 expect(screen.getByTestId('workspace-qa-path').textContent).toBe('/data/project/QA.md');
 setValue({...value(),qa_document_status:{state:'error',error:'disk full'}});
 expect(screen.getByRole('alert').textContent).toContain('disk full');
 setValue({...value(),qa_document_status:{state:'saved'}});
 expect(screen.queryByRole('alert')).toBeNull();
 expect(screen.getByTestId('workspace-sidebar').querySelector('[data-testid="workspace-qa"]')).toBeNull();
 setValue({...value(),qa_markdown:'# Workspace QA\n\nOwner chose bound A.'});
 expect(screen.getByTestId('workspace-qa').textContent).toContain('Owner chose bound A');
 expect(onAction).not.toHaveBeenCalledWith('member_inspect',{member_id:'qa'});
 await fireEvent.click(screen.getByTestId('workspace-member-lead'));
 expect(screen.getByText('workspace_get')).toBeTruthy();
 const allow=screen.getByText('Allow');
 await fireEvent.click(allow);
 expect(onAnswer).toHaveBeenCalledWith('approval-1','allow');
});

test('attention shows unselected teammate approvals, owner decisions and save failures before the plan', async () => {
 const f=frame();f.approvals=[{id:'pending',member_id:'lead',tool:'Read',subject:'notes',age_ms:0}];
 f.workspace!.messages=[{id:'decision',sender:'lead',recipient:'human',type:'decision_request',body:'Choose?',delivery:'recorded'}];
 f.qa_document_status={state:'error',error:'disk full'};
 const [value,setValue]=createSignal(f);const onAction=vi.fn();
 render(()=><WorkspaceLayout frame={value()} onAction={onAction}>Conversation</WorkspaceLayout>);
 const attention=screen.getByTestId('workspace-attention');
 expect(attention.compareDocumentPosition(screen.getByTestId('workspace-plan-link')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
 expect(attention.textContent).toContain('Approval needed');expect(attention.textContent).toContain('Choose?');expect(attention.textContent).toContain('disk full');
 await fireEvent.click(screen.getByTestId('workspace-approval-pending'));
 expect(onAction).toHaveBeenLastCalledWith('member_inspect',{member_id:'lead'});
 await fireEvent.click(screen.getByTestId('workspace-qa-save-error'));
 expect(screen.getByRole('tab',{name:/Questions/}).getAttribute('aria-selected')).toBe('true');
 setValue({...value(),approvals:[],qa_document_status:{state:'saved'},workspace:{...value().workspace!,messages:[]}});
 expect(screen.queryByTestId('workspace-attention')).toBeNull();
});

// Two levels instead of bare codes: members group under their package's
// title, unpackaged ones first, and a role-only name gets its code on the tab.
test('the team groups members under named packages', async () => {
 const f = frame();
 f.workspace!.packages = {CT1: {title: 'Console input-flood test'}};
 f.workspace!.members.push(
  {id:'ct1-impl',name:'Implementer',package:'CT1',role:'implementer',session_id:'s2',provider:'claude',lifetime:'resident',state:'available'},
  {id:'g1-red',name:'Red team',package:'G1',role:'reviewer',session_id:'s3',provider:'claude',lifetime:'resident',state:'available'});
 f.workspace!.qa = [{id:'q',package:'CT1',title:'Flake budget?',assignee:'lead',state:'open',blocking:false,revision:1}];
 render(() => <WorkspaceLayout frame={f} onAction={vi.fn()}>Conversation</WorkspaceLayout>);
 const sections = [...screen.getByTestId('workspace-sidebar').querySelectorAll('section[data-testid^="workspace-team-"]')].map(s => s.getAttribute('data-testid'));
 expect(sections).toEqual(['workspace-team-coordination', 'workspace-team-CT1', 'workspace-team-G1']);
 expect(screen.getByTestId('workspace-team-CT1').textContent).toContain('CT1 · Console input-flood test');
 expect(screen.getByTestId('workspace-team-CT1').contains(screen.getByTestId('workspace-member-ct1-impl'))).toBe(true);
 expect(screen.getByTestId('workspace-team-G1').textContent).toContain('G1');
 expect(screen.getByTestId('workspace-question-q').textContent).toContain('CT1 · Console input-flood test');
 await fireEvent.click(screen.getByTestId('workspace-member-ct1-impl'));
 expect(screen.getByRole('tab', {name: /CT1 · Implementer/})).toBeTruthy();
});

test('a member brief renders as Markdown above its turns, on a divider that remembers its place', async () => {
 localStorage.removeItem('wash.agent.workspace.member.split');
 const f = frame();
 f.workspace!.assignments = [{id:'a1',member_id:'lead',text:'## Timer\nUse a **monotonic** clock.',state:'completed',result:'- done\n- tests pass'}];
 render(() => <WorkspaceLayout frame={f} onAction={vi.fn()}>Conversation</WorkspaceLayout>);
 await fireEvent.click(screen.getByTestId('workspace-member-lead'));
 const brief = screen.getByTestId('workspace-assignment-a1');
 expect(brief.textContent).toContain('monotonic');
 expect(brief.textContent).not.toContain('**');
 expect(brief.textContent).not.toContain('## ');
 const panes = screen.getByTestId('workspace-member-panes');
 expect(panes.style.gridTemplateRows).toContain('35fr');
 const divider = screen.getByTestId('workspace-member-splitter');
 await fireEvent.keyDown(divider, {key:'ArrowDown'});
 expect(panes.style.gridTemplateRows).toContain('37fr');
 expect(localStorage.getItem('wash.agent.workspace.member.split')).toBe('37');
 await fireEvent.keyDown(divider, {key:'End'});
 expect(divider.getAttribute('aria-valuenow')).toBe('85');
});
