import { fileURLToPath } from 'node:url';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { test, expect } from '../fixtures/router';
import { closeButtonOf } from '../fixtures/agents';

const FAKE_DIR=fileURLToPath(new URL('../../out/e2e',import.meta.url));
test.use({routerOpts:{apps:['session','about','agentd','agents','ai','notify'],extraEnv:{PATH:`${FAKE_DIR}:${process.env.PATH??''}`,WASH_FAKE_WORKSPACE:'1'}}});

test('MCP configures a live workspace, collaborates across idle turns, and uninstalls cleanly',async({page,router})=>{
 test.setTimeout(90_000);
 await page.goto(router.url);
 await expect(page.locator('wash-app-session')).toBeVisible();
 const started=await router.controlRequest({t:'launch',app_id:'com.wash.ai'});
 await router.controlRequest({t:'msg',instance_id:String(started.instance_id),data:{kind:'agent_start',claim:true,agent:'codex',cwd:router.xdgConfigHome,prompt:''}});
 const app=page.locator('wash-app-ai');
 const composer=app.locator('[data-testid="agent-composer"]').first();
 await expect(composer).toBeEnabled();
 await expect(app.locator('[data-testid="workspace-sidebar"]')).toHaveCount(0);
 const state=()=>JSON.parse(readFileSync(join(router.xdgStateHome,'wash/workspaces.json'),'utf8')).workspaces.at(-1);
 const tool=async(name:string,args:object={})=>{
  if (await app.getByRole('tab',{name:'Conversation',exact:true}).count()) await app.getByRole('tab',{name:'Conversation',exact:true}).click();
  await composer.fill(`workspace ${name} ${JSON.stringify(args)}`);await composer.press('Enter');
 };
 const planFile=join(router.xdgConfigHome,'.wash','plan.toml');
 await tool('workspace_configure',{workspace:{name:'Redoubt test'},plan_file:planFile});
 await tool('plan_set',{nodes:{timer:{title:'Build timer'}}});
 const sidebar=app.locator('[data-testid="workspace-sidebar"]');
 await expect(sidebar).toBeVisible();
 await tool('plan_set',{nodes:{timer:{state:'active',emoji:'🔨'}}});
 await sidebar.locator('[data-testid="workspace-plan-link"]').click();
 await expect(app.locator('[data-testid="workspace-plan-state-timer"]')).toHaveText('active');
 await expect.poll(()=>readFileSync(planFile,'utf8')).toContain('state = "active"');
 await tool('workspace_configure',{members:{architect:{name:'Architect',instructions:'Review clock designs.',lifetime:'resident'}}});
 await expect.poll(()=>state().members.length).toBe(2);
 const resident=state().members.find((m:any)=>m.name==='Architect').id;
 await expect.poll(()=>state().messages.filter((m:any)=>m.recipient===resident&&m.delivery==='delivered').length).toBe(1);
 expect(state().members.find((m:any)=>m.id===resident).initial_configs.model).toBe('fast');
 await tool('message_send',{recipient:resident,type:'question',body:'Which clock?'});
 await expect.poll(()=>state().messages.some((m:any)=>m.type==='answer'&&m.body==='Fixture answer')).toBe(true);
 await tool('workspace_configure',{members:{implementer:{name:'Implementer',node:'timer',instructions:'Implement an assigned timer.',lifetime:'ephemeral',task:'WAIT_FOR_ANSWER'}}});
 await expect.poll(()=>state().members.find((m:any)=>m.name==='Implementer')?.state).toBe('ended');
 expect(state().assignments[0].state).toBe('completed');
 expect(state().assignments[0].result).toBe('Completed after an inbox reply');
 const retired=state().members.find((m:any)=>m.name==='Implementer').id;
 await expect(sidebar.locator(`[data-testid="workspace-usage-${retired}"]`)).toContainText('2,048 / 32,000 tokens');
 await sidebar.locator(`[data-testid="workspace-member-${retired}"]`).click();
 await expect(app.locator('[data-testid="workspace-member-detail"]')).toContainText('Archived conversation');
 await expect(app.locator('[data-testid="workspace-member-detail"]')).toContainText('Completed after an inbox reply');
 expect(state().members.find((m:any)=>m.id===resident).state).toBe('available');
 await tool('member_control',{action:'pause',member_ids:[resident]});
 await expect.poll(()=>state().members.find((m:any)=>m.id===resident).state).toBe('paused');
 await tool('message_send',{recipient:resident,type:'instruction',body:'Retained while paused'});
 await expect.poll(()=>state().messages.find((m:any)=>m.body==='Retained while paused')?.delivery).toBe('queued');
 await sidebar.locator(`[data-testid="workspace-member-${resident}"]`).click();
 await expect(app.locator('[data-testid="workspace-member-detail"]')).toBeVisible();
 await expect(page.locator('wash-app-ai')).toHaveCount(1);
 await app.getByLabel('Message Architect').fill('Human follow-up');
 await app.getByRole('button',{name:'Send message',exact:true}).click();
 await expect.poll(()=>state().messages.find((m:any)=>m.body==='Human follow-up')?.sender).toBe('human');
 await app.getByRole('button',{name:'Resume member',exact:true}).click();
 await expect.poll(()=>state().messages.find((m:any)=>m.body==='Human follow-up')?.delivery).toBe('delivered');
 expect(state().messages.find((m:any)=>m.body==='Retained while paused').delivery).toBe('delivered');
 await expect(app.locator('[data-testid="workspace-member-detail"]')).toContainText('Human follow-up');
 await tool('member_update',{status:'Review complete',emoji:'✅'});
 await expect(sidebar).toContainText('Review complete');
 // The orchestrator's own question is answered in the panel above its composer.
 await tool('decision_request',{questions:[{question:'Ship the timer?',options:[{label:'Yes'},{label:'No'}]}]});
 const panel=app.getByTestId('question-panel');
 await panel.getByRole('radio',{name:/Yes/}).click();await panel.getByRole('button',{name:/^Submit/}).click();
 await expect(panel).toHaveCount(0);
 await expect.poll(()=>state().messages.some((m:any)=>m.type==='decision_response'&&m.sender==='human')).toBe(true);
 await tool('flash_message',{text:'Timer milestone reached',emoji:'🎉'});
 await router.controlRequest({t:'launch',app_id:'com.wash.about'});
 await expect(page.locator('wash-app-about')).toBeVisible();
 const flash=page.locator('[data-testid="notification"]').filter({hasText:'Timer milestone reached'});
 await expect(flash).toBeVisible();
 const focusCursor=router.logCursor();
 await flash.click();
 await router.waitForLog(/agentd: focus key=.*raising controller=/,10_000,focusCursor);
 expect(state().plan.find((n:any)=>n.id==='timer').state).toBe('reported');
 await tool('plan_set',{nodes:{timer:{state:'done'}}});
 await sidebar.locator('[data-testid="workspace-plan-link"]').click();
 await expect(app.locator('[data-testid="workspace-plan-state-timer"]')).toHaveText('done');
 await page.reload();
 // Reload restores the About window used to check flash visibility in front.
 await closeButtonOf(page,page.locator('wash-app-about')).click();
 await expect(page.locator('wash-app-about')).toHaveCount(0);
 await expect(sidebar).toBeVisible();
 await sidebar.locator('[data-testid="workspace-plan-link"]').click();
 await expect(app.locator('[data-testid="workspace-plan-state-timer"]')).toHaveText('done');
 await tool('workspace_end');
 await expect(sidebar).toHaveCount(0);
 expect(readFileSync(planFile,'utf8')).toContain('state = "done"');
 expect(state().state).toBe('ended');
 await tool('workspace_configure',{workspace:{name:'Another project'}});
 await expect(sidebar).toContainText('Another project');
 await expect(page.locator('wash-app-ai')).toHaveCount(1);
});


test('MCP reads workspace JSON and launches members from a catalog with model-dependent effort', async ({ page, router }) => {
 test.setTimeout(90_000);
 // A curated catalog of the fake's models: smart (effort low or high) and
 // fast (low only). The orchestrator starts on Codex's own list; the
 // workspace switches to this one.
 mkdirSync(join(router.xdgConfigHome, 'wash'), { recursive: true });
 const slot = (model: string, effort: string) => ({ provider: 'codex', model, effort });
 writeFileSync(join(router.xdgConfigHome, 'wash', 'agents.json'), JSON.stringify({ catalogs: { fake: { name: 'Fake', slots: { frontier: slot('smart', 'high'), coding: slot('fast', 'low'), small: slot('fast', 'low') } } } }));
 await page.goto(router.url);
 await expect(page.locator('wash-app-session')).toBeVisible();
 const started = await router.controlRequest({ t: 'launch', app_id: 'com.wash.ai' });
 await router.controlRequest({ t: 'msg', instance_id: String(started.instance_id), data: { kind: 'agent_start', claim: true, agent: 'codex', cwd: router.xdgConfigHome, prompt: '' } });
 const app = page.locator('wash-app-ai');
 const composer = app.locator('[data-testid="agent-composer"]').first();
 const transcript = app.locator('[data-testid="agent-transcript"]').first();
 const outputs = transcript.getByText(/^WORKSPACE_(RESULT|ERROR) /);
 await expect(composer).toBeEnabled();
 const tool = async (name: string, args: object = {}, error = false) => {
  const count = await outputs.count();
  if (await app.getByRole('tab',{name:'Conversation',exact:true}).count()) await app.getByRole('tab',{name:'Conversation',exact:true}).click();
  await composer.fill(`workspace ${name} ${JSON.stringify(args)}`);
  await composer.press('Enter');
  await expect(outputs).toHaveCount(count + 1);
  const text = await outputs.last().innerText();
  expect(text, `MCP ${name} response`).toMatch(error ? /^WORKSPACE_ERROR / : /^WORKSPACE_RESULT /);
  return error ? text : JSON.parse(text.slice('WORKSPACE_RESULT '.length));
 };
 const about = await tool('workspace_get', {view:'about'});
 expect(about.server).toBe('wash_workspace');
 expect(about.caller.role).toBe('unattached');
 expect(about.instructions).toContain('END YOUR TURN');
 expect(about.capabilities.bulk_workspace_configuration).toBe(true);
 expect(about.capabilities.catalogs).toBe(true);
 expect(about.permissions.filesystem_enforcement).toMatch(/^unknown:/);
 expect(await tool('workspace_get')).toBeNull();
 await expect(app.locator('[data-testid="workspace-sidebar"]')).toHaveCount(0);
 await tool('workspace_configure', { workspace:{name: 'Catalog'} });
 expect((await tool('workspace_get', {view:'about'})).caller.role).toBe('orchestrator');
 const before = await tool('workspace_get', {view:'state'});
 // Started on the adapter alone: the workspace is on its own list.
 expect(before.workspace.catalog).toBe('openai');
 expect(before.sessions[before.workspace.orchestrator].config_options.map((c: any) => c.category)).toEqual(['model', 'thought_level']);
 await tool('workspace_configure', { expected_revision: before.workspace.revision, catalog: 'fake' });
 const resident = (await tool('workspace_configure', {members:{architect:{ name: 'Architect', model: 'frontier', instructions: 'Wait for design questions.', lifetime: 'resident' }}})).launches.architect;
 expect(resident.catalog).toBe('fake');
 expect(resident.model).toBe('frontier');
 expect(resident.initial_configs).toEqual({ model: 'smart', reasoning_effort: 'high' });
 const sidebar = app.locator('[data-testid="workspace-sidebar"]');
 await sidebar.locator(`[data-testid="workspace-member-${resident.id}"]`).click();
 await expect(app.locator('[data-testid="workspace-member-launch"]')).toHaveText('Launched: fake · frontier slot · codex · smart · effort high');
 // No model: the catalog's default slot. A member's own settings sit on
 // top of the slot's; a member may name another catalog outright.
 const helper = (await tool('workspace_configure', {members:{helper:{ name: 'Helper', instructions: 'Wait for work.', lifetime: 'resident' }}})).launches.helper;
 expect(helper.initial_configs).toEqual({ model: 'smart', reasoning_effort: 'high' });
 const override = (await tool('workspace_configure', {members:{reviewer:{ name: 'Reviewer', model: 'coding', instructions: 'Wait for a review.', lifetime: 'resident' }}})).launches.reviewer;
 expect(override.initial_configs).toEqual({ model: 'fast', reasoning_effort: 'low' });
 const own = (await tool('workspace_configure', {members:{own:{ name: 'Own', catalog: 'openai', model: 'smart', effort: 'high', instructions: 'Wait.', lifetime: 'resident' }}})).launches.own;
 expect(own.catalog).toBe('openai');
 expect(own.initial_configs).toEqual({ model: 'smart', reasoning_effort: 'high' });
 await tool('workspace_configure', {members:{unknown:{ name: 'Unknown', catalog: 'missing', instructions: 'Must not launch.', lifetime: 'resident' }}}, true);
 const failedLaunch = await tool('workspace_configure', {members:{invalid:{ name: 'Invalid effort', model: 'coding', effort: 'high', instructions: 'Must not run.', lifetime: 'resident' }}});
 expect(failedLaunch.launches.invalid.state).toBe('failed');
 expect(failedLaunch.launches.invalid.error).toBeTruthy();
 const after = await tool('workspace_get', { view: 'state', include_messages: true });
 expect(after.workspace.catalog).toBe('fake');
 expect(after.workspace.members.find((m: any) => m.id === resident.id).launch_settings).toMatchObject({ model: 'smart', effort: 'high' });
 expect(after.sessions[resident.id].config_options.find((c: any) => c.id === 'model').currentValue).toBe('smart');
 expect(after.workspace.members.some((m: any) => m.name === 'Unknown')).toBe(false);
 const failed = after.workspace.members.find((m: any) => m.name === 'Invalid effort');
 expect(failed.state).toBe('failed');
 expect(after.workspace.messages.some((m: any) => m.recipient === failed.id)).toBe(false);
 await tool('workspace_configure', { expected_revision: before.workspace.revision, name: 'Stale edit' }, true);
 // Back to the adapter's own list: later launches only.
 await tool('workspace_configure', { catalog: 'openai' });
 const switched = await tool('workspace_get', {view:'state'});
 expect(switched.workspace.catalog).toBe('openai');
 expect(switched.workspace.members.find((m: any) => m.id === resident.id).catalog).toBe('fake');
 expect(switched.workspace.name).toBe('Catalog');
 await tool('workspace_end');
 await expect(sidebar).toHaveCount(0);
});

test('sidebar shows live context and activity, and human messages remain distinct', async ({page,router}) => {
 test.setTimeout(45_000);
 await page.goto(router.url);
 await expect(page.locator('wash-app-session')).toBeVisible();
 const started=await router.controlRequest({t:'launch',app_id:'com.wash.ai'});
 await router.controlRequest({t:'msg',instance_id:String(started.instance_id),data:{kind:'agent_start',claim:true,agent:'codex',cwd:router.xdgConfigHome,prompt:''}});
 const app=page.locator('wash-app-ai');
 const composer=app.locator('[data-testid="agent-composer"]').first();
 await expect(composer).toBeEnabled();
 await composer.fill('workspace workspace_configure {"workspace":{"name":"Telemetry"}}');
 await composer.press('Enter');
 const sidebar=app.locator('[data-testid="workspace-sidebar"]');
 await expect(sidebar).toBeVisible();
 const state=()=>JSON.parse(readFileSync(join(router.xdgStateHome,'wash/workspaces.json'),'utf8')).workspaces.at(-1);
 const lead=state().orchestrator;
 const dot=sidebar.locator(`[data-testid="workspace-activity-${lead}"]`);
 await expect(dot).toHaveAttribute('data-activity','idle');
 await composer.fill('workspace_activity');await composer.press('Enter');
 await expect(dot).toHaveAttribute('data-activity','thinking');
 await expect(sidebar.locator(`[data-testid="workspace-usage-${lead}"]`)).toContainText('14,689 / 258,400 tokens');
 await expect(dot).toHaveCSS('animation-name','wash-workspace-pulse');
 await page.emulateMedia({reducedMotion:'reduce'});
 await expect(dot).toHaveCSS('animation-name','none');
 await expect(dot).toHaveAttribute('data-activity','tool');
 await expect(sidebar).toContainText('Running test suite');
 await expect(dot).toHaveAttribute('data-activity','responding');
 await expect(dot).toHaveAttribute('data-activity','idle');
 await expect.poll(()=>state().members[0].usage?.used).toBe(14689);
 const human=app.locator('[data-testid="agent-human-message"]').last();
 await expect(human).toHaveText('workspace_activity');
 await expect(human).toHaveCSS('font-weight','600');
 const colors=await human.evaluate(el=>({fg:getComputedStyle(el).color,bg:getComputedStyle(el).backgroundColor}));
 expect(colors.bg).not.toBe('rgba(0, 0, 0, 0)');
 await page.screenshot({path:test.info().outputPath('workspace-activity.png')});
 await composer.fill('workspace member_update {"waiting":{"reason":"Awaiting next instruction"}}');await composer.press('Enter');
 await expect(dot).toHaveAttribute('data-activity','waiting-message');
 await expect(dot).toHaveAttribute('data-pulse','false');
 await page.reload();
 await expect(sidebar.locator(`[data-testid="workspace-usage-${lead}"]`)).toContainText('2,048 / 32,000 tokens');
});


test('workspace tabs preserve drafts and the sidebar resizes without overflowing', async ({page,router}) => {
 test.setTimeout(60_000);
 await page.goto(router.url);
 await expect(page.locator('wash-app-session')).toBeVisible();
 const started=await router.controlRequest({t:'launch',app_id:'com.wash.ai'});
 await router.controlRequest({t:'msg',instance_id:String(started.instance_id),data:{kind:'agent_start',claim:true,agent:'codex',cwd:router.xdgConfigHome,prompt:''}});
 const app=page.locator('wash-app-ai');
 const main=app.getByTestId('workspace-main');
 const sidebar=app.getByTestId('workspace-sidebar');
 const composer=app.getByTestId('agent-composer');
 const outputs=main.getByText(/^WORKSPACE_RESULT /);
 const tool=async(name:string,args:object={})=>{
  const conversation=app.getByRole('tab',{name:'Conversation',exact:true});
  if(await conversation.count()) await conversation.click();
  const count=await outputs.count();
  await composer.fill(`workspace ${name} ${JSON.stringify(args)}`);await composer.press('Enter');
  await expect(outputs).toHaveCount(count+1);
  return JSON.parse((await outputs.last().innerText()).slice('WORKSPACE_RESULT '.length));
 };
 await expect(composer).toBeEnabled();
 await tool('workspace_configure',{workspace:{name:'Tab navigation'}});
 await tool('plan_set',{nodes:{clock:{title:'Verify timer clock',state:'active'}}});
 const member=(await tool('workspace_configure',{members:{architect:{name:'Architect',lifetime:'resident',instructions:'Wait for a design question.'}}})).launches.architect;
 const other=(await tool('workspace_configure',{members:{reviewer:{name:'Reviewer',lifetime:'resident',instructions:'Wait for review.'}}})).launches.reviewer;
 await composer.fill('Keep this conversation draft');
 await sidebar.getByTestId('workspace-plan-link').click();
 await expect(main.getByTestId('workspace-plan-node-clock')).toContainText('Verify timer clock');
 await expect(sidebar.getByTestId('workspace-plan-node-clock')).toHaveCount(0);
 await sidebar.getByTestId(`workspace-member-${member.id}`).click();
 await expect(main.getByTestId('workspace-member-detail')).toBeVisible();
 await expect(sidebar.getByTestId('workspace-member-detail')).toHaveCount(0);
 await app.getByLabel('Message Architect').fill('Keep this architect draft');
 await sidebar.getByTestId(`workspace-member-${other.id}`).click();
 await app.getByLabel('Message Reviewer').fill('Keep this reviewer draft');
 await sidebar.getByTestId(`workspace-member-${member.id}`).click();
 await expect(app.getByLabel('Message Architect')).toHaveValue('Keep this architect draft');
 await sidebar.getByTestId(`workspace-member-${member.id}`).click();
 await expect(app.getByRole('tab',{name:/Architect/})).toHaveCount(1);
 await app.getByRole('tab',{name:'Conversation',exact:true}).click();
 await expect(composer).toHaveValue('Keep this conversation draft');
 await app.getByRole('tab',{name:/Reviewer/}).click();
 await expect(app.getByLabel('Message Reviewer')).toHaveValue('Keep this reviewer draft');
 const divider=app.getByRole('separator',{name:'Resize workspace sidebar'});
 const initial=(await sidebar.boundingBox())!;
 const grip=(await divider.boundingBox())!;
 await page.mouse.move(grip.x+grip.width/2,grip.y+30);await page.mouse.down();
 await page.mouse.move(grip.x-90,grip.y+30,{steps:8});await page.mouse.up();
 await expect.poll(async()=>(await sidebar.boundingBox())!.width).toBeGreaterThan(initial.width+60);
 await divider.focus();await divider.press('ArrowRight');
 const saved=await divider.getAttribute('aria-valuenow');
 await expect.poll(async()=>app.getByTestId('workspace-layout').evaluate(el=>el.scrollWidth-el.clientWidth)).toBeLessThanOrEqual(1);
 await page.screenshot({path:test.info().outputPath('workspace-tabs.png')});
 await app.getByRole('tab',{name:/Reviewer/}).press('Delete');
 await expect(app.getByRole('tab',{name:/Reviewer/})).toHaveCount(0);
 await expect(app.getByRole('tab',{name:/Architect/})).toBeFocused();
 await app.getByRole('tab',{name:/Architect/}).press('Home');
 await expect(composer).toHaveValue('Keep this conversation draft');
 await page.reload();
 await expect(divider).toHaveAttribute('aria-valuenow',saved!);
 // Conversation and the Plan tab beside it; member tabs do not survive a reload.
 await expect(app.getByRole('tab')).toHaveCount(2);
 await tool('workspace_end',{confirm:true});
 await expect(sidebar).toHaveCount(0);
 await expect(app.getByRole('tablist',{name:'Workspace views'})).toHaveCount(0);
 await expect(composer).toBeEnabled();
 await expect(page.locator('wash-app-ai')).toHaveCount(1);
});


test('bulk workspace setup keeps package workers resident and QA survives refresh with owner attribution', async ({page,router}) => {
 test.setTimeout(90_000);
 await page.goto(router.url);await expect(page.locator('wash-app-session')).toBeVisible();
 const started=await router.controlRequest({t:'launch',app_id:'com.wash.ai'});
 await router.controlRequest({t:'msg',instance_id:String(started.instance_id),data:{kind:'agent_start',claim:true,agent:'codex',cwd:router.xdgConfigHome,prompt:''}});
 const app=page.locator('wash-app-ai'),sidebar=app.getByTestId('workspace-sidebar');
 const composer=app.getByTestId('agent-composer').first(),outputs=app.getByTestId('agent-transcript').first().getByText(/^WORKSPACE_(RESULT|ERROR) /);
 const tool=async(name:string,args:object={},error=false)=>{
  const tab=app.getByRole('tab',{name:'Conversation',exact:true});if(await tab.count())await tab.click();
  const count=await outputs.count();await composer.fill(`workspace ${name} ${JSON.stringify(args)}`);await composer.press('Enter');await expect(outputs).toHaveCount(count+1);
  const text=await outputs.last().innerText();expect(text).toMatch(error?/^WORKSPACE_ERROR /:/^WORKSPACE_RESULT /);return error?text:JSON.parse(text.slice('WORKSPACE_RESULT '.length));
 };
 await expect(composer).toBeEnabled();
 const about=await tool('workspace_get',{view:'about'});expect(about.tools).toHaveLength(14);
 expect(about.tools).not.toContain('member_spawn');
 await tool('member_spawn',{},true);
 await tool('setup_workspace',{name:'Removed'},true);
 await expect(sidebar).toHaveCount(0);
expect(about.caller.config_options.length).toBeGreaterThan(0);
 const qaDir=join(router.xdgConfigHome,'qa');const qaPath=join(qaDir,'K5-bound.md');
 const setup={qa_dir:qaDir,request_id:'package-setup',workspace:{name:'Package QA'}};
 await tool('workspace_configure',{...setup,preview:true});await expect(sidebar).toHaveCount(0);
 expect((await tool('workspace_configure',setup)).qa_document_status.state).toBe('saved');
 await tool('plan_set',{nodes:{K5:{title:'K5 package'}}});
 const config={request_id:'package-members',members:{
  implementer:{name:'K5 implementer',model:'fast',effort:'low',lifetime:'resident',node:'K5',role:'implementer',instructions:'Implement only the assigned package.',task:'First delivery'},
  red:{name:'K5 red',model:'fast',effort:'low',lifetime:'resident',node:'K5',role:'reviewer',instructions:'Review defensively; wait for work.'},
 }};
 const configured=await tool('workspace_configure',config);
 expect(configured.launches.implementer.state).toBe('available');expect(configured.launches.red.state).toBe('available');
 const state=()=>JSON.parse(readFileSync(join(router.xdgStateHome,'wash/workspaces.json'),'utf8')).workspaces.at(-1);
 await expect.poll(()=>state().assignments[0]?.state).toBe('completed');
 expect(state().members.find((m:any)=>m.key==='implementer').state).toBe('available');
 const repeated=await tool('workspace_configure',config);expect(repeated.receipt.members).toEqual(configured.receipt.members);expect(state().members).toHaveLength(3);
 await tool('assignment_update',{request_id:'fix-pass',updates:[{action:'create',member_id:'implementer',text:'Follow-up fix'}]});
 await expect.poll(()=>state().assignments[1]?.state).toBe('completed');expect(state().members).toHaveLength(3);
 await tool('message_send',{recipient:'red',type:'instruction',body:'ASK_PERMISSION'});
 await expect(sidebar.getByTestId('workspace-attention')).toContainText('Approval needed');
 await sidebar.getByTestId('workspace-attention').getByRole('button',{name:/Approval needed/}).click();
 await expect(app.getByTestId('workspace-member-detail').getByRole('button',{name:/^Allow(\s|$)/})).toBeVisible();
 await app.getByTestId('workspace-member-detail').getByRole('button',{name:/^Allow(\s|$)/}).click();
 await expect(app.getByTestId('workspace-member-detail')).toContainText('Permission outcome: allow');
 await tool('message_send',{request_id:'open-qa',recipient:'red',type:'question',body:'Does the clock meet the bound?',qa:{id:'K5-bound',action:'open',node:'K5',title:'Wakeup bound',blocking:true}});
 await sidebar.getByTestId('workspace-question-K5-bound').click();
 await expect(app.getByTestId('workspace-qa')).toContainText('Does the clock meet the bound?');
 await expect(app.getByTestId('workspace-qa-path')).toHaveText(qaDir);
 expect(readFileSync(qaPath,'utf8')).toContain('Does the clock meet the bound?');
 await expect.poll(()=>state().qa[0].events.some((e:any)=>e.author===configured.receipt.members.red&&e.body==='Fixture answer')).toBe(true);
 await tool('decision_request',{questions:[{question:'Choose the bound',options:[{label:'A'},{label:'B'}],recommended:'A'}],thread_id:'K5-bound',request_id:'owner-choice'});
 const panel=app.getByTestId('question-panel');
 await panel.getByRole('radio',{name:/^1 A/}).click();await panel.getByLabel('Your answer: Choose the bound').fill('Use bound A');
 await panel.getByRole('button',{name:/^Submit/}).click();
 await expect.poll(()=>state().qa[0].events.some((e:any)=>e.author==='human'&&e.body.includes('A — Use bound A'))).toBe(true);
 await expect.poll(()=>readFileSync(qaPath,'utf8')).toContain('Use bound A');
 await page.reload();await expect(sidebar).toBeVisible();await sidebar.getByTestId('workspace-qa-link').click();
 await expect(app.getByTestId('workspace-qa')).toContainText('Use bound A');await expect(app.getByTestId('workspace-qa')).toContainText('Owner');
 const qa=await tool('workspace_get',{view:'qa',thread_id:'K5-bound'});
 await tool('member_update',{qa_updates:[{id:'K5-bound',action:'resolve',expected_revision:qa.thread.revision-1,evidence:'stale'}]},true);
 await tool('member_update',{request_id:'resolve-qa',status:'Package accepted',qa_updates:[{id:'K5-bound',action:'resolve',expected_revision:qa.thread.revision,evidence:'Regression tests passed; review complete.'}]});
 await expect(sidebar.getByTestId('workspace-qa-link')).toContainText('0 open');
 await tool('member_control',{action:'end',node:'K5'});
 await expect.poll(()=>state().members.filter((m:any)=>m.node==='K5').every((m:any)=>m.state==='ended')).toBe(true);
 expect(state().qa[0].state).toBe('resolved');
 expect(readFileSync(qaPath,'utf8')).toContain('Status: **resolved**');
 await sidebar.getByTestId('workspace-qa-link').click();await page.screenshot({path:test.info().outputPath('workspace-qa.png')});
 // K5 is reported, not accepted: ending with it is a decision.
 expect(await tool('workspace_end',{},true)).toContain('K5');
 const final=await tool('workspace_end',{confirm:true});expect(final.qa_document_status.state).toBe('saved');await expect(sidebar).toHaveCount(0);
 await tool('workspace_configure',{workspace:{name:'Resumed package QA'},qa_dir:qaDir});
 await sidebar.getByTestId('workspace-qa-link').click();
 // A resolved thread resumes as a header; its events stay in its file.
 await expect(app.getByTestId('workspace-qa')).toContainText('Regression tests passed; review complete.');
 await expect(app.getByTestId('workspace-qa')).toContainText('earlier workspace');
 expect(state().qa[0].state).toBe('resolved');expect(state().qa[0].archived).toBe(true);
 expect((await tool('workspace_get',{view:'qa',thread_id:'K5-bound'})).thread.events.some((e:any)=>e.body.includes('Use bound A'))).toBe(true);
 await tool('workspace_end',{confirm:true});await expect(sidebar).toHaveCount(0);
});

// A workspace takes its catalog from the orchestrator's own session, and a
// member launched with "model":"small" runs that catalog's small slot with
// no model string in its definition. The catalog is agents.json's own, with
// the fake's models, so the slot's model and effort are checkable here.
test('a member launched by slot inherits the orchestrator\'s catalog', async ({page,router}) => {
 test.setTimeout(90_000);
 mkdirSync(join(router.xdgConfigHome,'wash'),{recursive:true});
 const slot=(model:string,effort:string)=>({provider:'codex',model,effort});
 writeFileSync(join(router.xdgConfigHome,'wash','agents.json'),JSON.stringify({catalogs:{fake:{name:'Fake',slots:{frontier:slot('smart','high'),coding:slot('fast','low'),small:slot('smart','low')}}}}));
 await page.goto(router.url);
 await expect(page.locator('wash-app-session')).toBeVisible();
 const cursor=router.logCursor();
 const started=await router.controlRequest({t:'launch',app_id:'com.wash.ai'});
 await router.controlRequest({t:'msg',instance_id:String(started.instance_id),data:{kind:'agent_start',claim:true,catalog:'fake',model:'frontier',cwd:router.xdgConfigHome,prompt:''}});
 await router.waitForLog(/agentd: session settings key=\S+ catalog=fake model=frontier connection= adapter=codex mode=\S* yolo=false effective=map\[model:smart reasoning_effort:high\]/,25_000,cursor);
 const app=page.locator('wash-app-ai');
 const composer=app.locator('[data-testid="agent-composer"]').first();
 await expect(composer).toBeEnabled();
 // The store file appears with the first workspace.
 const state=()=>{try{return JSON.parse(readFileSync(join(router.xdgStateHome,'wash/workspaces.json'),'utf8')).workspaces.at(-1);}catch{return undefined;}};
 const tool=async(name:string,args:object={})=>{await composer.fill(`workspace ${name} ${JSON.stringify(args)}`);await composer.press('Enter');};
 await tool('workspace_configure',{workspace:{name:'Slots'},members:{rev:{name:'Reviewer',model:'small',instructions:'Review when asked.',lifetime:'resident'}}});
 await expect.poll(()=>state()?.members?.find((m:any)=>m.key==='rev')?.state,{timeout:30_000}).toBe('available');
 const w=state();
 expect(w.catalog).toBe('fake');
 const rev=w.members.find((m:any)=>m.key==='rev');
 expect(rev.model).toBe('small');
 expect(rev.catalog).toBe('fake');
 expect(rev.launch_settings).toMatchObject({provider:'codex',model:'smart',effort:'low'});
 expect(rev.initial_configs).toMatchObject({model:'smart',reasoning_effort:'low'});
});

// Yolo on the orchestrator is yolo for the workspace: a member launched
// with no approval of its own starts auto-approved because its launcher is,
// and follows the orchestrator's later toggle; one that says "ask" does
// not. The member records in workspaces.json are the durable form, and the
// members' own sessions are told (log).
test('members without their own approval follow the orchestrator\'s yolo', async ({page,router}) => {
 test.setTimeout(90_000);
 await page.goto(router.url);
 await expect(page.locator('wash-app-session')).toBeVisible();
 const started=await router.controlRequest({t:'launch',app_id:'com.wash.ai'});
 // Started with yolo on, the way the launcher's Permissions row does it.
 await router.controlRequest({t:'msg',instance_id:String(started.instance_id),data:{kind:'agent_start',claim:true,agent:'codex',cwd:router.xdgConfigHome,prompt:'',yolo:true}});
 const app=page.locator('wash-app-ai');
 const composer=app.locator('[data-testid="agent-composer"]').first();
 await expect(composer).toBeEnabled();
 await expect(app.locator('[data-testid="agent-yolo-badge"]')).toBeVisible({timeout:15_000});
 const state=()=>{try{return JSON.parse(readFileSync(join(router.xdgStateHome,'wash/workspaces.json'),'utf8')).workspaces.at(-1);}catch{return undefined;}};
 const member=(key:string)=>state()?.members?.find((m:any)=>m.key===key);
 const tool=async(name:string,args:object={})=>{await composer.fill(`workspace ${name} ${JSON.stringify(args)}`);await composer.press('Enter');};
 let cursor=router.logCursor();
 await tool('workspace_configure',{workspace:{name:'Yolo'},members:{
  follows:{name:'Follows',instructions:'Wait.',lifetime:'resident'},
  asks:{name:'Asks',approval:'ask',instructions:'Wait.',lifetime:'resident'}}});
 await expect.poll(()=>member('follows')?.state,{timeout:30_000}).toBe('available');
 await expect.poll(()=>member('asks')?.state,{timeout:30_000}).toBe('available');
 expect(member('follows').auto_approve).toBe(true);
 expect(member('asks').auto_approve).toBeFalsy();
 await router.waitForLog(/agentd: acp yolo key=\S+ on=true why="the session that launched this member is auto-approved"/,15_000,cursor);

 // The orchestrator switches yolo off: the follower follows, the asker
 // was never on.
 cursor=router.logCursor();
 await app.locator('[data-testid="ai-menubar-session"]').click();
 await page.locator('[data-testid="ai-menu-yolo"]').click();
 await router.waitForLog(/agentd: acp yolo key=\S+ on=false why="the orchestrator's auto-approval changed"/,15_000,cursor);
 await expect.poll(()=>member('follows')?.auto_approve,{timeout:15_000}).toBeFalsy();
 expect(member('asks').auto_approve).toBeFalsy();
});
