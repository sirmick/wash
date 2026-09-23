import { afterEach, expect, test, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library';
import { createSignal } from 'solid-js';
import { WorkspaceSidebar, type WorkspaceFrame } from './WorkspaceSidebar.tsx';

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
 const onAction=vi.fn(); render(() => <WorkspaceSidebar frame={frame()} onAction={onAction}/>);
 expect(screen.getByTestId('workspace-item-timer').textContent).toContain('active');
 await fireEvent.click(screen.getByText('Design notes'));
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
 render(() => <WorkspaceSidebar frame={value()} onAction={()=>{}}/>);
 await fireEvent.click(screen.getByTestId('workspace-member-lead'));
 await fireEvent.input(screen.getByLabelText('Message Architect'),{target:{value:'Unsaved feedback'}});
 setValue({...value(),workspace:{...value().workspace!,revision:2,items:[{id:'timer',text:'Build timer',state:'done',revision:2}]}});
 expect(screen.getByTestId('workspace-item-timer').textContent).toContain('done');
 expect((screen.getByLabelText('Message Architect') as HTMLTextAreaElement).value).toBe('Unsaved feedback');
 await fireEvent.click(screen.getByText('Design notes'));
 setValue({...value(),document_text:'# New design\nClock verified.'});
 expect(screen.getByTestId('workspace-document').textContent).toContain('Clock verified');
});

test('decisions and paused member recovery have explicit controls', async () => {
 const f=frame();f.workspace!.members[0].state='paused';f.workspace!.messages=[{id:'q',sender:'lead',recipient:'human',type:'decision_request',body:'Ship?',delivery:'recorded'}];
 const onAction=vi.fn();render(()=> <WorkspaceSidebar frame={f} onAction={onAction}/>);
 await fireEvent.input(screen.getByLabelText('Decision response'),{target:{value:'Proceed'}});
 await fireEvent.click(screen.getByText('Answer'));
 expect(onAction).toHaveBeenCalledWith('decision_response',{id:'q',body:'Proceed'});
 await fireEvent.click(screen.getByTestId('workspace-member-lead'));
 await fireEvent.click(screen.getByText('Resume member'));
 expect(onAction).toHaveBeenCalledWith('member_resume',{member_id:'lead'});
});


test('live activity and usage update independently of the plan and retain unknown counts', async () => {
 const [value,setValue]=createSignal(frame());
 render(() => <WorkspaceSidebar frame={value()} onAction={()=>{}}/>);
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
