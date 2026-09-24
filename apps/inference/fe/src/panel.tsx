import { For, Show, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import type { JSX } from 'solid-js';
import { Input, Row, Section, SmallBtn, ServiceBadge, defineSettingsPanel, tokens, washCopyText } from '@wash/ui';
import type { SettingsPanelProps } from '@wash/ui';

type Connection = { id:string; name:string; adapter:string; base_url?:string; model?:string; has_credential?:boolean; available:boolean; detail?:string };
type State = { default?:string; connections?:Connection[] };

const Panel = (props: SettingsPanelProps) => {
  const [state,setState]=createSignal<State>({connections:[]});
  const [selected,setSelected]=createSignal('');
  const [baseURL,setBaseURL]=createSignal('');
  const [model,setModel]=createSignal('');
  const [key,setKey]=createSignal('');
  const [status,setStatus]=createSignal('');
  const [promptText,setPromptText]=createSignal('');
  const [response,setResponse]=createSignal('');
  const [promptError,setPromptError]=createSignal('');
  const [promptMeta,setPromptMeta]=createSignal('');
  const [promptBusy,setPromptBusy]=createSignal(false);
  let activeRequest='';
  let requestSeq=0;
  const current=createMemo(()=>state().connections?.find(c=>c.id===selected()));
  const hydrate=(id:string)=>{const c=state().connections?.find(v=>v.id===id);setSelected(id);setBaseURL(c?.base_url??'');setModel(c?.model??'');setKey('');setStatus('')};
  onMount(()=>{
    const off=props.port.onMessage((m)=>{
      if(m.kind==='state'){const s=(m.state??{}) as State;setState(s);if((s.default||'')!==selected())hydrate(s.default||'')}
      if(m.kind==='config.saved')setStatus('Saved');
      if(m.kind==='config.error')setStatus(String(m.msg||'Could not save'));
      if(String(m.id||'')!==activeRequest)return;
      if(m.kind==='inference.result'){
        setPromptBusy(false);setResponse(String(m.text||''));setPromptError('');
        const usage=(m.usage||{}) as Record<string,unknown>;
        const counts=[usage.input_tokens&&`${usage.input_tokens} in`,usage.output_tokens&&`${usage.output_tokens} out`].filter(Boolean).join(' · ');
        setPromptMeta([m.provider,m.model,`${m.elapsed_ms} ms`,counts].filter(Boolean).join(' · '));activeRequest='';
      }
      if(m.kind==='inference.error'){setPromptBusy(false);setResponse('');setPromptError(`${String(m.code||'error')}: ${String(m.msg||'Inference failed')}`);setPromptMeta('');activeRequest=''}
      if(m.kind==='inference.cancel_ok'){setPromptBusy(false);setPromptMeta('Cancelled');activeRequest=''}
    });
    props.port.send({kind:'subscribe'});
    onCleanup(()=>{if(activeRequest)props.port.send({kind:'inference.cancel',id:activeRequest});props.port.send({kind:'unsubscribe'});off()});
  });
  const save=()=>{const payload:Record<string,unknown>={kind:'config.save',connection_id:selected(),base_url:baseURL(),model:model()};if(key())payload.api_key=key();props.port.send(payload);setStatus('Saving…')};
  const chooseDefault=(id:string)=>{props.port.send({kind:'config.select',connection_id:id});setState(s=>({...s,default:id}))};
  const choose=(id:string)=>{hydrate(id);chooseDefault(id);setStatus(id?'Provider selected':'Inference is off')};
  const runPrompt=()=>{if(!promptText().trim()||promptBusy())return;activeRequest=`settings-${Date.now()}-${++requestSeq}`;setPromptBusy(true);setResponse('');setPromptError('');setPromptMeta('');props.port.send({kind:'inference.start',id:activeRequest,purpose:'settings-test',input:[{type:'text',text:promptText()}],max_output_tokens:2048})};
  const cancelPrompt=()=>{if(activeRequest)props.port.send({kind:'inference.cancel',id:activeRequest})};
  return <div style={rootStyle}>
    <Section title="AI provider">
      <Row label="Provider"><select value={selected()} onInput={e=>choose(e.currentTarget.value)} style={selectStyle}><option value="">Off — no provider</option><For each={state().connections}>{c=><option value={c.id}>{c.name}</option>}</For></select></Row>
      <Show when={current()} fallback={<div style={helpStyle}>AI features are off. Choose a provider to configure and use it.</div>}>
      <Show when={current()?.adapter==='openai'}>
        <Row label="Base URL"><Input value={baseURL()} onInput={e=>setBaseURL(e.currentTarget.value)} placeholder="http://127.0.0.1:11434/v1" /></Row>
      </Show>
      <Row label="Model"><Input value={model()} onInput={e=>setModel(e.currentTarget.value)} placeholder={current()?.adapter==='openai'?'Required':'Provider default'} /></Row>
      <Show when={current()?.adapter==='openai'}><Row label="API key"><div style={actionsStyle}><Input type="password" value={key()} onInput={e=>setKey(e.currentTarget.value)} placeholder={current()?.has_credential?'Saved — leave blank to keep':'Optional for local servers'} style={{flex:1}}/><Show when={current()?.has_credential}><SmallBtn onClick={()=>{setKey('');props.port.send({kind:'config.save',connection_id:selected(),base_url:baseURL(),model:model(),api_key:''})}}>Remove</SmallBtn></Show></div></Row></Show>
      <Row label="Availability"><ServiceBadge tone={current()?.available?'on':'off'} label={current()?.available?(current()?.detail||'configured'):'not configured'} /></Row>
      <div style={actionsStyle}><SmallBtn onClick={save}>Save configuration</SmallBtn><Show when={status()}><span style={statusStyle}>{status()}</span></Show></div>
      <div style={helpStyle}>Inference sends app-provided text to this connection. Ollama loopback traffic stays on this box; hosted endpoints receive it over the network.</div>
      </Show>
    </Section>
    <Section title="Test inference">
      <textarea value={promptText()} onInput={e=>setPromptText(e.currentTarget.value)} onKeyDown={e=>{if(e.key==='Enter'&&(e.ctrlKey||e.metaKey)){e.preventDefault();runPrompt()}}} placeholder="Enter a one-shot prompt…" style={promptStyle}/>
      <div style={actionsStyle}><SmallBtn onClick={runPrompt}>{promptBusy()?'Running…':'Test inference'}</SmallBtn><Show when={promptBusy()}><SmallBtn onClick={cancelPrompt}>Cancel</SmallBtn></Show><Show when={response()}><SmallBtn onClick={()=>washCopyText(response())}>Copy response</SmallBtn></Show><span style={statusStyle}>{promptMeta()}</span></div>
      <Show when={promptError()}><div style={errorStyle}>{promptError()}</div></Show>
      <Show when={response()}><pre style={responseStyle}>{response()}</pre></Show>
      <div style={helpStyle}>Ctrl+Enter runs the prompt. Prompt text and responses are not saved by wash.</div>
    </Section>
  </div>;
};
const rootStyle:JSX.CSSProperties={display:'flex','flex-direction':'column',gap:'20px',padding:'4px'};
const helpStyle:JSX.CSSProperties={opacity:0.7,font:tokens.type.textMd,'line-height':1.5,'max-width':'500px'};
const actionsStyle:JSX.CSSProperties={display:'flex',gap:'8px','align-items':'center','flex-wrap':'wrap'};
const statusStyle:JSX.CSSProperties={opacity:0.75,font:tokens.type.textMd};
const selectStyle:JSX.CSSProperties={background:tokens.bgMenu,color:tokens.fg,border:`1px solid ${tokens.borderMenu}`,'border-radius':tokens.radiusMd,padding:'4px 8px',font:tokens.type.textMd};
const promptStyle:JSX.CSSProperties={width:'100%','box-sizing':'border-box','min-height':'100px',resize:'vertical',background:tokens.bgInset,color:tokens.fg,border:`1px solid ${tokens.borderMenu}`,'border-radius':tokens.radiusMd,padding:'10px',font:tokens.type.monoMd,outline:'none','margin-bottom':'8px'};
const responseStyle:JSX.CSSProperties={margin:'8px 0',padding:'10px',background:tokens.bgInset,border:`1px solid ${tokens.borderMenu}`,'border-radius':tokens.radiusMd,'white-space':'pre-wrap','word-break':'break-word','user-select':'text',font:tokens.type.textMd,'line-height':1.5,'max-height':'260px',overflow:'auto'};
const errorStyle:JSX.CSSProperties={color:tokens.fgDanger,font:tokens.type.textMd,'white-space':'pre-wrap','margin-top':'8px'};
defineSettingsPanel('wash-settings-panel-inference',Panel);
