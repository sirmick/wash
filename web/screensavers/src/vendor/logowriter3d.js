// LogoWriter GL 3D: a 3D turtle-graphics screensaver, as an ES module.
// MIT license. No dependencies, no build step.
//
// Importing this module has no side effects (no DOM access, no listeners, no globals).
// Call mount(container, opts) to run the screensaver inside an element; see README "Embedding".
//
// Layout: Logo compiler (tokenizer, parser, Logo->JS codegen) -> 3D turtle runtime (one per mount)
// -> program playlist -> WebGL 2 renderer (one per mount) -> camera -> mount() director/HUD.

// ======================= Logo compiler =======================
class LogoError extends Error{constructor(msg,line){super(msg);this.line=line||0}}

const WS=/\s/, WORDCH=/[A-Za-z0-9_.?!]/;
function tokenize(src){
  const out=[];let i=0,line=1;const n=src.length;
  while(i<n){
    const c=src[i];
    if(c==='\n'){line++;i++;continue}
    if(WS.test(c)){i++;continue}
    if(c===';'){while(i<n&&src[i]!=='\n')i++;continue}
    if(c==='['||c===']'||c==='('||c===')'){out.push({t:c,line});i++;continue}
    if(c==='-'){
      const p=i>0?src[i-1]:' ',q=src[i+1];
      if((WS.test(p)||p==='['||p==='(')&&q!==undefined&&!WS.test(q)){
        if(/[0-9.]/.test(q)){const m=/^-(\d*\.?\d+(?:[eE][+-]?\d+)?)/.exec(src.slice(i,i+48));if(m){out.push({t:'num',v:-parseFloat(m[1]),line});i+=m[0].length;continue}}
        out.push({t:'neg',line});i++;continue;
      }
      out.push({t:'op',v:'-',line});i++;continue;
    }
    if(c==='<'||c==='>'){let v=c;if(src[i+1]==='=')v+='=';else if(c==='<'&&src[i+1]==='>')v='<>';out.push({t:'op',v,line});i+=v.length;continue}
    if(c==='+'||c==='*'||c==='/'||c==='='){out.push({t:'op',v:c,line});i++;continue}
    if(/[0-9]/.test(c)||(c==='.'&&/[0-9]/.test(src[i+1]||''))){
      const m=/^\d*\.?\d+(?:[eE][+-]?\d+)?/.exec(src.slice(i,i+48));out.push({t:'num',v:parseFloat(m[0]),line});i+=m[0].length;continue;
    }
    if(c==='"'||c===':'){
      let j=i+1;while(j<n&&WORDCH.test(src[j]))j++;
      const w=src.slice(i+1,j);if(!w)throw new LogoError(`Expected a name after ${c}`,line);
      out.push({t:c==='"'?'quote':'var',v:w,line});i=j;continue;
    }
    if(WORDCH.test(c)){let j=i;while(j<n&&WORDCH.test(src[j]))j++;const w=src.slice(i,j);out.push({t:'word',v:w.toUpperCase(),raw:w,line});i=j;continue}
    throw new LogoError(`Unexpected character "${c}"`,line);
  }
  return out;
}
function group(toks){
  const root=[],stack=[root],opens=[];
  for(const t of toks){
    if(t.t==='['){const l={t:'list',items:[],line:t.line};stack[stack.length-1].push(l);stack.push(l.items);opens.push(t)}
    else if(t.t===']'){if(stack.length===1)throw new LogoError('Unexpected ]',t.line);stack.pop();opens.pop()}
    else stack[stack.length-1].push(t);
  }
  if(stack.length>1)throw new LogoError('Missing ] for the [ opened on this line',opens[opens.length-1].line);
  return root;
}
const ident=s=>s.toLowerCase().replace(/[^a-z0-9_]/g,c=>'$'+c.charCodeAt(0));
const vid=name=>'v_'+ident(name), pid=name=>'p_'+ident(name);
const num=v=>v<0||Object.is(v,-0)?`(${v})`:String(v);

// Primitive table. kind: op (outputs) | cmd. mv: turtle step (costs budget, may yield). v: variadic in parens.
const PRIMS={};
function def(names,n,kind,gen,o){for(const nm of names.split(' '))PRIMS[nm]={n,kind,gen,...(o||{})}}
const j=(a,op)=>`(${a.join(op)})`;
def('SUM',2,'op',a=>j(a,'+'),{v:1}); def('DIFFERENCE',2,'op',a=>j(a,'-'));
def('PRODUCT',2,'op',a=>j(a,'*'),{v:1}); def('QUOTIENT',2,'op',a=>j(a,'/'));
def('REMAINDER',2,'op',a=>j(a,'%')); def('MODULO',2,'op',a=>`R.mod(${a})`);
def('MINUS',1,'op',a=>`(-${a[0]})`); def('ABS',1,'op',a=>`$abs(${a})`); def('SQRT',1,'op',a=>`$sqrt(${a})`);
def('POWER',2,'op',a=>`$pow(${a})`); def('EXP',1,'op',a=>`$exp(${a})`); def('LN',1,'op',a=>`$log(${a})`);
def('LOG10',1,'op',a=>`$log10(${a})`);
def('SIN',1,'op',a=>`$sin((${a})*DEG)`); def('COS',1,'op',a=>`$cos((${a})*DEG)`); def('TAN',1,'op',a=>`$tan((${a})*DEG)`);
def('ARCTAN',1,'op',a=>`($atan(${a})/DEG)`); def('INT',1,'op',a=>`$trunc(${a})`); def('ROUND',1,'op',a=>`$round(${a})`);
def('RANDOM',1,'op',a=>`R.rnd(${a})`); def('PI',0,'op',()=>'$PI');
def('MAX',2,'op',a=>`$max(${a})`,{v:1}); def('MIN',2,'op',a=>`$min(${a})`,{v:1});
def('AND',2,'op',a=>`(!!${j(a,'&&')})`,{v:1}); def('OR',2,'op',a=>`(!!${j(a,'||')})`,{v:1}); def('NOT',1,'op',a=>`(!(${a}))`);
def('EQUALP',2,'op',a=>`R.eq(${a})`); def('LESSP',2,'op',a=>j(a,'<')); def('GREATERP',2,'op',a=>j(a,'>'));
def('XCOR',0,'op',()=>'T.x'); def('YCOR',0,'op',()=>'T.y'); def('HEADING',0,'op',()=>'R.hd(T)');
def('POS',0,'op',()=>'[T.x,T.y]'); def('PENDOWNP',0,'op',()=>'T.pen'); def('WHO',0,'op',()=>'T.id');
def('TURTLES',0,'op',()=>'R.turtles.length'); def('TIMER',0,'op',()=>'R.timer()');
def('TOWARDS',1,'op',a=>`R.towards(T,${a})`); def('DISTANCE',1,'op',a=>`R.distance(T,${a})`); def('POSOF',1,'op',a=>`R.posof(${a})`);
def('LIST',2,'op',a=>`[${a}]`,{v:1}); def('FIRST',1,'op',a=>`R.first(${a})`); def('LAST',1,'op',a=>`R.last(${a})`);
def('BUTFIRST BF',1,'op',a=>`R.bf(${a})`); def('BUTLAST BL',1,'op',a=>`R.bl(${a})`); def('ITEM',2,'op',a=>`R.item(${a})`);
def('COUNT',1,'op',a=>`R.count(${a})`); def('FPUT',2,'op',a=>`R.fput(${a})`); def('LPUT',2,'op',a=>`R.lput(${a})`);
def('SENTENCE SE',2,'op',a=>`R.se(${a})`,{v:1}); def('WORD',2,'op',a=>`R.word(${a})`,{v:1});
def('EMPTYP',1,'op',a=>`R.emptyp(${a})`); def('NUMBERP',1,'op',a=>`(typeof (${a})==='number')`);
// turtle commands
def('FORWARD FD',1,'cmd',a=>`R.fd(T,${a})`,{mv:1}); def('BACK BK',1,'cmd',a=>`R.fd(T,-(${a}))`,{mv:1});
def('RIGHT RT',1,'cmd',a=>`R.rt(T,${a})`,{mv:1}); def('LEFT LT',1,'cmd',a=>`R.rt(T,-(${a}))`,{mv:1});
def('PENUP PU',0,'cmd',()=>'T.pen=false'); def('PENDOWN PD',0,'cmd',()=>'T.pen=true,T.erase=false');
def('PENERASE PE',0,'cmd',()=>'T.pen=true,T.erase=true'); def('PENPAINT PPT',0,'cmd',()=>'T.pen=true,T.erase=false');
def('HOME',0,'cmd',()=>'R.home(T)',{mv:1}); def('CLEARSCREEN CS',0,'cmd',()=>'R.cs(T)',{mv:1}); def('CLEAN',0,'cmd',()=>'R.clean()');
def('SETXY',2,'cmd',a=>`R.setxy(T,${a})`,{mv:1}); def('SETX',1,'cmd',a=>`R.setxy(T,${a},T.y)`,{mv:1});
def('SETY',1,'cmd',a=>`R.setxy(T,T.x,${a})`,{mv:1}); def('SETPOS',1,'cmd',a=>`R.setpos(T,${a})`,{mv:1});
def('SETHEADING SETH',1,'cmd',a=>`R.seth(T,${a})`,{mv:1});
def('SETPC SETC SETPENCOLOR',1,'cmd',a=>`T.c=R.color(${a})`); def('SETW SETWIDTH SETPENSIZE',1,'cmd',a=>`R.setw(T,${a})`);
def('SETBG',1,'cmd',a=>`R.setbg(${a})`); def('HIDETURTLE HT',0,'cmd',()=>'T.vis=false'); def('SHOWTURTLE ST',0,'cmd',()=>'T.vis=true');
def('DOT',0,'cmd',()=>'R.dot(T)',{mv:1});
def('PRINT PR',1,'cmd',a=>`R.print(${a})`,{v:1}); def('SHOW',1,'cmd',a=>`R.show(${a})`); def('TYPE',1,'cmd',a=>`R.type(${a})`,{v:1});

const SPECIAL=new Set(['REPEAT','FOREVER','IF','IFELSE','WHILE','UNTIL','FOR','LAUNCH','MAKE','LOCAL','STOP','OUTPUT','OP','DIE','WAIT','TO','END','REPCOUNT','TRUE','FALSE']);
// Anything here means a procedure must be a coroutine (it moves turtles, sleeps or spawns).
const IMPURE=new Set(['LAUNCH','DIE','WAIT','FOREVER']);
for(const k in PRIMS)if(PRIMS[k].kind==='cmd'&&k!=='PRINT'&&k!=='PR'&&k!=='SHOW'&&k!=='TYPE')IMPURE.add(k);

const PRELUDE='const DEG=Math.PI/180,$sin=Math.sin,$cos=Math.cos,$tan=Math.tan,$atan=Math.atan,$sqrt=Math.sqrt,$abs=Math.abs,$pow=Math.pow,$exp=Math.exp,$log=Math.log,$log10=Math.log10,$trunc=Math.trunc,$round=Math.round,$min=Math.min,$max=Math.max,$PI=Math.PI;';

function splitProcs(toks){
  const procs=new Map();let i=0;
  while(i<toks.length){
    const t=toks[i];
    if(!(t.t==='word'&&t.v==='TO'))throw new LogoError('The flip side holds procedures only. Start one with TO name, or type commands in the command center',t.line);
    const nt=toks[i+1];
    if(!nt||nt.t!=='word')throw new LogoError('TO needs a procedure name',t.line);
    const name=nt.v;
    if(PRIMS[name]||SPECIAL.has(name))throw new LogoError(`${name} is a primitive. Pick another name`,t.line);
    if(procs.has(name))throw new LogoError(`${name} is defined twice`,t.line);
    i+=2;const params=[];
    while(i<toks.length&&toks[i].t==='var'&&toks[i].line===t.line){params.push(toks[i].v.toUpperCase());i++}
    const body=[];
    while(i<toks.length&&!(toks[i].t==='word'&&toks[i].v==='END')){
      if(toks[i].t==='word'&&toks[i].v==='TO')throw new LogoError(`${name} is missing END`,t.line);
      body.push(toks[i]);i++;
    }
    if(i>=toks.length)throw new LogoError(`${name} is missing END`,t.line);
    i++;
    procs.set(name,{name,params,body:group(body),flat:body,line:t.line});
  }
  return procs;
}
function analyze(procs){
  for(const p of procs.values()){
    p.words=new Set(p.flat.filter(t=>t.t==='word').map(t=>t.v));
    p.outputs=p.words.has('OUTPUT')||p.words.has('OP');
    p.pure=![...p.words].some(w=>IMPURE.has(w));
  }
  let ch=true;
  while(ch){ch=false;for(const p of procs.values())if(p.pure)for(const w of p.words){const q=procs.get(w);if(q&&!q.pure){p.pure=false;ch=true;break}}}
}
function listData(items){
  return items.map(t=>t.t==='num'?t.v:t.t==='word'?t.raw:t.t==='quote'?t.v:t.t==='var'?':'+t.v:t.t==='list'?listData(t.items):t.t==='op'?t.v:t.t==='neg'?'-':t.t);
}
const show=t=>t.t==='num'?String(t.v):t.t==='word'?t.raw:t.t==='quote'?'"'+t.v:t.t==='var'?':'+t.v:t.t==='list'?'[…]':t.t==='op'?t.v:t.t;

class Stream{constructor(a){this.a=a;this.i=0}peek(){return this.a[this.i]}next(){return this.a[this.i++]}eof(){return this.i>=this.a.length}last(){return this.a[this.i-1]}}
const CMP={'=':(a,b)=>`R.eq(${a},${b})`,'<>':(a,b)=>`(!R.eq(${a},${b}))`,'<':(a,b)=>`(${a}<${b})`,'>':(a,b)=>`(${a}>${b})`,'<=':(a,b)=>`(${a}<=${b})`,'>=':(a,b)=>`(${a}>=${b})`};

class Compiler{
  constructor(procs){this.procs=procs;this.K=[];this.uid=0}
  id(){return ++this.uid}
  lineOf(s){const t=s.peek()||s.last();return t?t.line:0}
  block(items,ctx){const s=new Stream(items);let js='';while(!s.eof())js+=this.stmt(s,ctx);return js}
  // Loop back-edge: coroutines spend budget and yield; pure code only guards against runaway loops.
  tick(ctx){return ctx.gen?'if(--T.b<=0)yield;':'if(++R.k>R.K)R.hang();'}
  codeList(s,ctx,who){
    const t=s.next();
    if(!t)throw new LogoError(`${who} needs more inputs`,this.lineOf(s));
    if(t.t!=='list')throw new LogoError(`${who} needs a [list] of instructions, got ${show(t)}`,t.line);
    return this.block(t.items,ctx);
  }
  cond(s,ctx,who){
    const t=s.peek();
    if(t&&t.t==='list'){s.next();const ls=new Stream(t.items);const e=this.expr(ls,ctx,who);if(!ls.eof())throw new LogoError(`Extra ${show(ls.peek())} in ${who}'s condition`,t.line);return e}
    return this.expr(s,ctx,who);
  }
  quoteName(s,who){const q=s.next();if(!q||q.t!=='quote')throw new LogoError(`${who} needs a "name`,q?q.line:this.lineOf(s));return q.v.toUpperCase()}
  varRef(name,ctx){const id=vid(name);return ctx.locals.has(id)?id:`R.gv(G.${id},"${name}")`}
  call(p,args,ctx){const f=`P.${pid(p.name)}(T${args.map(a=>','+a).join('')})`;return p.pure?f:`(yield* ${f})`}
  args(s,ctx,nm,n){const a=[];for(let i=0;i<n;i++)a.push(this.expr(s,ctx,nm));return a}
  close(s,open){const t=s.next();if(!t||t.t!==')')throw new LogoError('Missing )',open.line)}

  stmt(s,ctx){
    const t=s.next();
    if(t.t==='('){
      const w=s.peek();
      if(w&&w.t==='word'){const pr=PRIMS[w.v];if(pr&&pr.kind==='cmd'&&pr.v){s.next();const a=[];while(s.peek()&&s.peek().t!==')')a.push(this.expr(s,ctx,w.v));this.close(s,t);return pr.gen(a,ctx)+';'}}
      throw new LogoError("You don't say what to do with ( … )",t.line);
    }
    if(t.t!=='word')throw new LogoError(`You don't say what to do with ${show(t)}`,t.line);
    const nm=t.v;
    switch(nm){
      case 'REPEAT':{
        const n=this.expr(s,ctx,nm),r='r'+this.id(),e='e'+this.id();
        ctx.rc.push(r);ctx.locals.add(r);const body=this.codeList(s,ctx,nm);ctx.rc.pop();ctx.locals.delete(r);
        return `for(let ${r}=1,${e}=+(${n});${r}<=${e};${r}++){${body}${this.tick(ctx)}}`;
      }
      case 'FOREVER':return `for(;;){${this.codeList(s,ctx,nm)}${this.tick(ctx)}}`;
      case 'IF':{
        const c=this.expr(s,ctx,nm),a=this.codeList(s,ctx,nm);
        const el=s.peek()&&s.peek().t==='list'?`else{${this.codeList(s,ctx,nm)}}`:'';
        return `if(${c}){${a}}${el}`;
      }
      case 'IFELSE':{const c=this.expr(s,ctx,nm),a=this.codeList(s,ctx,nm),b=this.codeList(s,ctx,nm);return `if(${c}){${a}}else{${b}}`}
      case 'WHILE':case 'UNTIL':{
        const c=this.cond(s,ctx,nm),body=this.codeList(s,ctx,nm);
        return `while(${nm==='UNTIL'?'!':''}(${c})){${body}${this.tick(ctx)}}`;
      }
      case 'FOR':{
        const ct=s.next();
        if(!ct||ct.t!=='list')throw new LogoError('FOR needs [name start end] or [name start end step]',t.line);
        const ls=new Stream(ct.items),vt=ls.next();
        if(!vt||vt.t!=='word')throw new LogoError('FOR needs [name start end]',t.line);
        const v=vid(vt.v),a=this.expr(ls,ctx,'FOR'),b=this.expr(ls,ctx,'FOR'),st=ls.eof()?null:this.expr(ls,ctx,'FOR');
        const E='e'+this.id(),S='s'+this.id(),had=ctx.locals.has(v);
        ctx.locals.add(v);const body=this.codeList(s,ctx,nm);if(!had)ctx.locals.delete(v);
        return `{let ${v}=+(${a});const ${E}=+(${b}),${S}=${st?`+(${st})`:`(${v}<=${E}?1:-1)`};for(;${S}>=0?${v}<=${E}:${v}>=${E};${v}+=${S}){${body}${this.tick(ctx)}}}`;
      }
      case 'LAUNCH':{
        // Snapshot every visible local into the new turtle's closure (by value, like Logo's dynamic scope at launch time).
        const caps=[...ctx.locals];
        const ictx={gen:true,locals:new Set(caps),hoist:new Set(),rc:[...ctx.rc]};
        const body=this.codeList(s,ictx,nm);
        const hs=ictx.hoist.size?`let ${[...ictx.hoist].join(',')};`:'';
        return `R.launch(T,((${caps})=>function*(T){${hs}${body}})(${caps}));${this.tick(ctx)}`;
      }
      case 'MAKE':{const name=this.quoteName(s,nm),id=vid(name),e=this.expr(s,ctx,nm);return ctx.locals.has(id)?`${id}=${e};`:`G.${id}=${e};`}
      case 'LOCAL':{const id=vid(this.quoteName(s,nm));if(!ctx.locals.has(id)){ctx.locals.add(id);ctx.hoist.add(id)}return ''}
      case 'STOP':return 'return;';
      case 'OUTPUT':case 'OP':return `return ${this.expr(s,ctx,nm)};`;
      case 'DIE':return 'R.die(T);';
      case 'WAIT':return `R.wait(${this.expr(s,ctx,nm)});yield;`;
      case 'TO':case 'END':throw new LogoError(`${nm} only works on the flip side`,t.line);
      case 'REPCOUNT':case 'TRUE':case 'FALSE':throw new LogoError(`You don't say what to do with ${nm}`,t.line);
    }
    const pr=PRIMS[nm];
    if(pr){
      if(pr.kind==='op')throw new LogoError(`You don't say what to do with ${nm}`,t.line);
      return pr.gen(this.args(s,ctx,nm,pr.n),ctx)+';'+(pr.mv?this.tick(ctx):'');
    }
    const p=this.procs.get(nm);
    if(p)return this.call(p,this.args(s,ctx,nm,p.params.length),ctx)+';';
    throw new LogoError(`I don't know how to ${t.raw}`,t.line);
  }
  expr(s,ctx,who){if(s.eof()||s.peek().t===')')throw new LogoError(`${who} needs more inputs`,this.lineOf(s));return this.cmp(s,ctx,who)}
  cmp(s,ctx,who){let l=this.add(s,ctx,who);for(;;){const t=s.peek();if(t&&t.t==='op'&&CMP[t.v]){s.next();l=CMP[t.v](l,this.add(s,ctx,who))}else return l}}
  add(s,ctx,who){let l=this.mul(s,ctx,who);for(;;){const t=s.peek();if(t&&t.t==='op'&&(t.v==='+'||t.v==='-')){s.next();l=`(${l}${t.v}${this.mul(s,ctx,who)})`}else return l}}
  mul(s,ctx,who){let l=this.unary(s,ctx,who);for(;;){const t=s.peek();if(t&&t.t==='op'&&(t.v==='*'||t.v==='/')){s.next();l=`(${l}${t.v}${this.unary(s,ctx,who)})`}else return l}}
  unary(s,ctx,who){const t=s.peek();if(t&&t.t==='neg'){s.next();return `(-${this.unary(s,ctx,who)})`}return this.primary(s,ctx,who)}
  primary(s,ctx,who){
    const t=s.next();
    if(!t)throw new LogoError(`${who} needs more inputs`,this.lineOf(s));
    switch(t.t){
      case 'num':return num(t.v);
      case 'quote':{const n=Number(t.v);return isFinite(n)?num(n):JSON.stringify(t.v)}
      case 'var':return this.varRef(t.v.toUpperCase(),ctx);
      case 'list':this.K.push(listData(t.items));return `K[${this.K.length-1}]`;
      case '(':{
        const w=s.peek();
        if(w&&w.t==='word'){const pr=PRIMS[w.v];if(pr&&pr.v&&pr.kind==='op'){s.next();const a=[];while(s.peek()&&s.peek().t!==')')a.push(this.expr(s,ctx,w.v));this.close(s,t);return pr.gen(a,ctx)}}
        const e=this.expr(s,ctx,who);this.close(s,t);return `(${e})`;
      }
      case 'word':{
        const nm=t.v;
        if(nm==='REPCOUNT'){if(!ctx.rc.length)throw new LogoError('REPCOUNT only works inside REPEAT',t.line);return ctx.rc[ctx.rc.length-1]}
        if(nm==='TRUE')return 'true';
        if(nm==='FALSE')return 'false';
        const pr=PRIMS[nm];
        if(pr){if(pr.kind!=='op')throw new LogoError(`${nm} doesn't output a value, so ${who} can't use it`,t.line);return pr.gen(this.args(s,ctx,nm,pr.n),ctx)}
        const p=this.procs.get(nm);
        if(p){if(!p.outputs)throw new LogoError(`${nm} doesn't OUTPUT anything, so ${who} can't use it`,t.line);return this.call(p,this.args(s,ctx,nm,p.params.length),ctx)}
        if(SPECIAL.has(nm))throw new LogoError(`${nm} can't be used as an input to ${who}`,t.line);
        throw new LogoError(`I don't know how to ${t.raw}`,t.line);
      }
      default:throw new LogoError(`Unexpected ${show(t)} in ${who}`,t.line);
    }
  }
}
function compileProgram(src){
  const procs=splitProcs(tokenize(src));analyze(procs);
  const c=new Compiler(procs);let js='"use strict";'+PRELUDE;
  for(const p of procs.values()){
    const params=p.params.map(vid);
    const ctx={gen:!p.pure,locals:new Set(params),hoist:new Set(),rc:[]};
    const body=c.block(p.body,ctx);
    const hs=ctx.hoist.size?`let ${[...ctx.hoist].join(',')};`:'';
    js+=`P.${pid(p.name)}=function${p.pure?'':'*'}(T${params.map(x=>','+x).join('')}){${hs}${body}};\n`;
  }
  return {procs,js,K:c.K};
}
function compileCommand(src,procs){
  const c=new Compiler(procs);
  const ctx={gen:true,locals:new Set(),hoist:new Set(),rc:[]};
  const body=c.block(group(tokenize(src)),ctx);
  const hs=ctx.hoist.size?`let ${[...ctx.hoist].join(',')};`:'';
  return {js:'"use strict";'+PRELUDE+`return function*(T){${hs}${body}};`,K:c.K};
}

// ======================= 3D turtle extensions =======================
// Orientation is a frame: H (forward), L (left), U (up). Starts as the 2D turtle:
// facing +Y, left is -X, up is +Z (out of the screen), so 2D programs draw in the XY plane.
def('UP PITCHUP',1,'cmd',a=>`R.pitch(T,${a})`,{mv:1});
def('DOWN DN PITCHDOWN',1,'cmd',a=>`R.pitch(T,-(${a}))`,{mv:1});
def('ROLLRIGHT RR',1,'cmd',a=>`R.roll(T,${a})`,{mv:1});
def('ROLLLEFT RL',1,'cmd',a=>`R.roll(T,-(${a}))`,{mv:1});
def('SETXYZ',3,'cmd',a=>`R.setxyz(T,${a})`,{mv:1});
def('SETXY',2,'cmd',a=>`R.setxyz(T,${a},T.z)`,{mv:1});
def('SETX',1,'cmd',a=>`R.setxyz(T,${a},T.y,T.z)`,{mv:1});
def('SETY',1,'cmd',a=>`R.setxyz(T,T.x,${a},T.z)`,{mv:1});
def('SETZ',1,'cmd',a=>`R.setxyz(T,T.x,T.y,${a})`,{mv:1});
def('FACE',1,'cmd',a=>`R.face(T,${a})`,{mv:1});
def('ZCOR',0,'op',()=>'T.z');
def('POS',0,'op',()=>'[T.x,T.y,T.z]');
def('HUE',1,'op',a=>`R.hue(${a})`);
for(const k in PRIMS)if(PRIMS[k].kind==='cmd'&&!['PRINT','PR','SHOW','TYPE'].includes(k))IMPURE.add(k);

// ======================= Runtime: 3D turtles + scheduler =======================
const PALETTE=['#000000','#2b4bd6','#1f9a40','#22b2c6','#d33a2c','#b13fb3','#8a5a2b','#a9a9ad','#55565c','#6f8cff','#6fcf72','#8ff0ee','#f08b28','#ff7fae','#f2d23a','#ffffff']
  .map(h=>[1,3,5].map(k=>parseInt(h.slice(k,k+2),16)/255).concat(1));
// Black (the default pen) means "auto": the renderer colors it from a palette along the drawing order.
const AUTO=[0,0,0,1];
const DEG=Math.PI/180, MAXT=2048, DIE_SIGNAL={};
const FIELDS=['x','y','z','hx','hy','hz','lx','ly','lz','ux','uy','uz','pen','erase','c','w','vis'];
// Flat orientation with a compass heading, as SETH/HOME expect.
function flat(T,h){const r=h*DEG,s=Math.sin(r),c=Math.cos(r);T.hx=s;T.hy=c;T.hz=0;T.lx=-c;T.ly=s;T.lz=0;T.ux=0;T.uy=0;T.uz=1}
class Turtle{
  constructor(id,src){
    this.id=id;this.b=0;this.alive=true;
    if(src){for(const f of FIELDS)this[f]=src[f]}
    else{this.x=this.y=this.z=0;flat(this,0);this.pen=true;this.erase=false;this.c=AUTO;this.w=1;this.vis=true}
  }
}
// One runtime per mount: its own globals, turtles and task list. `sink` receives segments: {seg(x0,y0,z0,x1,y1,z1,c,w), clear()}.
function createRuntime(sink){
  const R={
    G:{},P:{},procs:new Map(),turtles:[],tasks:[],nextId:1,cur:null,k:0,K:2e8,t0:0,now:0,
    lines:[],out(s,cls){R.lines.push([s,cls||''])},
    gv(v,n){if(v===undefined)throw new LogoError(`${n} has no value`);return v},
    hang(){throw new LogoError('A calculation ran 200 million loop steps without drawing. Is a WHILE loop stuck?')},
    mod(a,b){return((a%b)+b)%b},
    rnd(n){return Math.floor(Math.random()*n)},
    hd(T){return((Math.atan2(T.hx,T.hy)/DEG)+360)%360},
    timer(){return R.now},
    eq(a,b){
      if(a===b)return true;
      if(typeof a==='number'||typeof b==='number')return a!==''&&b!==''&&+a===+b;
      if(typeof a==='string'&&typeof b==='string')return a.toUpperCase()===b.toUpperCase();
      if(Array.isArray(a)&&Array.isArray(b))return a.length===b.length&&a.every((x,i)=>R.eq(x,b[i]));
      return false;
    },
    flat,
    ortho(T){
      let n=Math.hypot(T.hx,T.hy,T.hz);T.hx/=n;T.hy/=n;T.hz/=n;
      const d=T.lx*T.hx+T.ly*T.hy+T.lz*T.hz;T.lx-=d*T.hx;T.ly-=d*T.hy;T.lz-=d*T.hz;
      n=Math.hypot(T.lx,T.ly,T.lz);T.lx/=n;T.ly/=n;T.lz/=n;
      T.ux=T.hy*T.lz-T.hz*T.ly;T.uy=T.hz*T.lx-T.hx*T.lz;T.uz=T.hx*T.ly-T.hy*T.lx;
    },
    line(T,x,y,z){if(T.pen&&!T.erase)sink.seg(T.x,T.y,T.z,x,y,z,T.c,T.w);T.x=x;T.y=y;T.z=z},
    fd(T,d){d=+d;R.line(T,T.x+d*T.hx,T.y+d*T.hy,T.z+d*T.hz)},
    // yaw left by a: H toward L
    rt(T,a){const r=-(+a)*DEG,c=Math.cos(r),s=Math.sin(r);
      const hx=T.hx*c+T.lx*s,hy=T.hy*c+T.ly*s,hz=T.hz*c+T.lz*s;
      T.lx=T.lx*c-T.hx*s;T.ly=T.ly*c-T.hy*s;T.lz=T.lz*c-T.hz*s;T.hx=hx;T.hy=hy;T.hz=hz;R.ortho(T)},
    // pitch up by a: H toward U
    pitch(T,a){const r=(+a)*DEG,c=Math.cos(r),s=Math.sin(r);
      const hx=T.hx*c+T.ux*s,hy=T.hy*c+T.uy*s,hz=T.hz*c+T.uz*s;
      T.ux=T.ux*c-T.hx*s;T.uy=T.uy*c-T.hy*s;T.uz=T.uz*c-T.hz*s;T.hx=hx;T.hy=hy;T.hz=hz;R.ortho(T)},
    // roll right by a: L toward U
    roll(T,a){const r=(+a)*DEG,c=Math.cos(r),s=Math.sin(r);
      const lx=T.lx*c+T.ux*s,ly=T.ly*c+T.uy*s,lz=T.lz*c+T.uz*s;
      T.ux=T.ux*c-T.lx*s;T.uy=T.uy*c-T.ly*s;T.uz=T.uz*c-T.lz*s;T.lx=lx;T.ly=ly;T.lz=lz;R.ortho(T)},
    face(T,l){
      if(!Array.isArray(l)||l.length<2)throw new LogoError('FACE needs a [x y z] list');
      const dx=+l[0]-T.x,dy=+l[1]-T.y,dz=(l.length>2?+l[2]:T.z)-T.z,n=Math.hypot(dx,dy,dz);
      if(n<1e-9)return;
      T.hx=dx/n;T.hy=dy/n;T.hz=dz/n;
      let lx=T.uy*T.hz-T.uz*T.hy,ly=T.uz*T.hx-T.ux*T.hz,lz=T.ux*T.hy-T.uy*T.hx;
      if(Math.hypot(lx,ly,lz)>1e-6){T.lx=lx;T.ly=ly;T.lz=lz}
      R.ortho(T);
    },
    seth(T,a){R.flat(T,+a)},
    setxyz(T,x,y,z){R.line(T,+x,+y,+z)},
    setpos(T,l){if(!Array.isArray(l)||l.length<2)throw new LogoError('SETPOS needs a [x y] or [x y z] list');R.setxyz(T,l[0],l[1],l.length>2?l[2]:T.z)},
    home(T){R.setxyz(T,0,0,0);R.flat(T,0)},
    cs(T){sink.clear();T.x=T.y=T.z=0;R.flat(T,0)},
    clean(){sink.clear()},
    dot(T){sink.seg(T.x,T.y,T.z,T.x,T.y,T.z,T.c,Math.max(+T.w,0.5))},
    setw(T,w){T.w=Math.max(0.1,+w)},
    color(v){
      if(typeof v==='number'||(typeof v==='string'&&isFinite(+v))){const n=Math.floor(+v);return PALETTE[((n%16)+16)%16]}
      if(Array.isArray(v)&&v.length>=3){const a=v.length>3?+v[3]/255:1;return[+v[0]/255,+v[1]/255,+v[2]/255,a]}
      throw new LogoError(`Colors are a number 0–15 or a [r g b] list, not ${R.fmt(v,true)}`);
    },
    hue(d){d=+d*DEG;return[128+127*Math.cos(d),128+127*Math.cos(d-120*DEG),128+127*Math.cos(d+120*DEG)]},
    setbg(){},
    fmt(x,br){
      if(typeof x==='number')return Number.isInteger(x)?String(x):String(+x.toPrecision(8));
      if(x===true)return 'TRUE';if(x===false)return 'FALSE';if(x===undefined)return '';
      if(Array.isArray(x)){const s=x.map(e=>R.fmt(e,true)).join(' ');return br?`[${s}]`:s}
      return String(x);
    },
    print(...a){R.out(a.map(x=>R.fmt(x,false)).join(' '))},
    type(...a){R.out(a.map(x=>R.fmt(x,false)).join(''))},
    show(x){R.out(R.fmt(x,true))},
    wait(n){R.cur.wake=R.frameNo+Math.max(0,+n)},
    frameNo:0,
    launch(T,f){
      if(R.turtles.length>=MAXT)throw new LogoError(`Too many turtles (the limit is ${MAXT})`);
      const nt=new Turtle(R.nextId++,T);R.turtles.push(nt);
      R.tasks.push({T:nt,gen:f(nt),wake:0,done:false});
    },
    die(T){if(T.id===0)T.vis=false;else T.alive=false;throw DIE_SIGNAL},
    towards(T,l){return((Math.atan2(+l[0]-T.x,+l[1]-T.y)/DEG)+360)%360},
    posof(n){const T=R.turtles.find(t=>t.id===+n&&t.alive);if(!T)throw new LogoError(`There is no turtle ${n}`);return[T.x,T.y,T.z]},
    distance(T,l){return Math.hypot(+l[0]-T.x,+l[1]-T.y,(l.length>2?+l[2]:T.z)-T.z)},
    first(x){if(!x.length)throw new LogoError('FIRST of an empty list');return x[0]},
    last(x){if(!x.length)throw new LogoError('LAST of an empty list');return x[x.length-1]},
    bf(x){return Array.isArray(x)?x.slice(1):String(x).slice(1)},
    bl(x){return Array.isArray(x)?x.slice(0,-1):String(x).slice(0,-1)},
    item(i,x){const v=x[(+i)-1];if(v===undefined)throw new LogoError(`ITEM ${i} is past the end`);return v},
    count(x){return Array.isArray(x)?x.length:String(x).length},
    fput(a,l){return[a,...l]},lput(a,l){return[...l,a]},
    se(...a){return[].concat(...a.map(x=>Array.isArray(x)?x:[x]))},
    word(...a){return a.map(x=>R.fmt(x,false)).join('')},
    emptyp(x){return Array.isArray(x)?x.length===0:x===''},
  };
  function instantiate(js,K,P){return new Function('R','G','P','K',js)(R,R.G,P,K)}
  function resetWorld(){R.tasks=[];R.nextId=1;R.G={};R.turtles=[new Turtle(0)];sink.clear()}
  function loadProgram(src){
    const prog=compileProgram(src);const P={};
    instantiate(prog.js,prog.K,P);
    R.P=P;R.procs=prog.procs;
    return prog;
  }
  function startTask(src,T){
    const cmd=compileCommand(src,R.procs);
    const f=new Function('R','G','P','K',cmd.js)(R,R.G,R.P,cmd.K);
    T=T||R.turtles[0];
    R.tasks.push({T,gen:f(T),wake:0,done:false});
  }
  function step(task,budget){
    if(task.done||task.wake>R.frameNo)return 0;
    const T=task.T;T.b=budget;R.cur=task;
    try{const r=task.gen.next();if(r.done)task.done=true}
    catch(e){task.done=true;if(e!==DIE_SIGNAL)throw e}
    return budget-T.b;
  }
  return {R,resetWorld,loadProgram,startTask,step};
}

// ======================= Programs =======================
// 3D Hilbert curve from the L-system in The Algorithmic Beauty of Plants, translated to Logo procedures.
function lsysLogo(rules,map){
  let s='';
  for(const [k,body] of Object.entries(rules)){
    const parts=[];
    for(const ch of body){if(rules[ch])parts.push(`H${ch} :n - 1`);else if(map[ch])parts.push(map[ch])}
    const lines=[];let cur='';
    for(const p of parts){if((cur+' '+p).length>60){lines.push(cur);cur=p}else cur=cur?cur+' '+p:p}
    if(cur)lines.push(cur);
    s+=`\nTO H${k} :n\n  IF :n = 0 [STOP]\n${lines.map(l=>'  '+l).join('\n')}\nEND\n`;
  }
  return s;
}
const HILBERT=`; A 3D Hilbert curve: one unbroken path that visits every
; point of an 8 x 8 x 8 grid exactly once. The four
; procedures HA..HD are the L-system from The Algorithmic
; Beauty of Plants, with UP/DOWN for pitch and RR/RL for roll.

TO STARTUP
  HT SETW 7
  MAKE "s 56
  MAKE "k 0
  HA 3
END

TO STEP
  SETPC HUE 200 + :k * 0.9
  FD :s
  MAKE "k :k + 1
END
`+lsysLogo({
  A:'B-F+CFC+F-D&F^D-F+&&CFC+F+B//',
  B:'A&F^CFB^F^D^^-F-D^|F^B|FC^F^A//',
  C:'|D^|F^B-F+C^F^A&&FA&F^C+F+B^F^D//',
  D:'|CFB-F+B|FA&F^A&&FB-F+B|FC//',
},{F:'STEP','+':'LT 90','-':'RT 90','&':'DOWN 90','^':'UP 90','\\':'RL 90','/':'RR 90','|':'RT 180'});

const PROGRAM_DEFS=[
{id:'gyro-bloom',name:'Gyro bloom',cam:'orbit',speed:14,src:`; Thirty-six petals, each made of two quarter circles.
; Between petals the turtle rolls 10 degrees around its own
; heading, so the flower closes into a sphere.

TO STARTUP
  HT SETW 2
  REPEAT 36 [
    SETPC HUE REPCOUNT * 10
    PETAL
    RR 10
  ]
END

TO PETAL
  REPEAT 2 [
    REPEAT 90 [FD 3 RT 1]
    RT 90
  ]
END
`},
{id:'hilbert-cube',name:'Hilbert cube',cam:'chase',speed:2,src:HILBERT},
{id:'torus-knots',name:'Torus knots',cam:'orbit',speed:5,src:`; Three torus knots, nested, drawn by three turtles at once.
; A (p, q) knot winds p times around the torus axis and
; q times through the hole.

TO STARTUP
  HT
  LAUNCH [KNOT 3 7 260 70 0]
  LAUNCH [KNOT 2 5 150 45 120]
  LAUNCH [KNOT 5 8 380 40 240]
END

TO KNOT :p :q :big :small :hue
  LOCAL "t
  SETW 5
  PU SETXYZ :big + :small 0 0 PD
  FOR [t 0 360 0.25] [
    SETPC HUE :hue + :t
    SETXYZ (:big + :small * COS :q * :t) * COS :p * :t
           :small * SIN :q * :t
           (:big + :small * COS :q * :t) * SIN :p * :t
  ]
END
`},
{id:'lorenz-attractor',name:'Lorenz attractor',cam:'drift',speed:12,src:`; Six turtles integrate the Lorenz system from starting
; points 0.01 apart. Within seconds the paths separate, and
; all of them trace the same butterfly.

TO STARTUP
  HT
  REPEAT 6 [LAUNCH [FLY REPCOUNT]]
END

TO FLY :k
  LOCAL "x LOCAL "y LOCAL "z LOCAL "dx LOCAL "dy LOCAL "dz
  MAKE "x 0.1 + :k * 0.01 MAKE "y 0 MAKE "z 0
  SETPC HUE :k * 60
  SETW 1.5
  PU
  REPEAT 9000 [
    MAKE "dx 10 * (:y - :x)
    MAKE "dy :x * (28 - :z) - :y
    MAKE "dz :x * :y - 8 / 3 * :z
    MAKE "x :x + :dx * 0.004
    MAKE "y :y + :dy * 0.004
    MAKE "z :z + :dz * 0.004
    SETXYZ :x * 12 (:z - 25) * 12 :y * 12
    IF REPCOUNT = 200 [PD]
  ]
END
`},
{id:'spring-tree',name:'Spring tree',cam:'drift',speed:0.35,src:`; Every branch launches three turtles for the branches
; above it and then dies, so the whole tree grows at once.
; Each new branch pitches up, and they roll 120 degrees apart.

TO STARTUP
  HT PU SETY -330 PD
  BRANCH 150 7
END

TO BRANCH :len :n
  LOCAL "a
  SETW :n * 1.6
  SETPC (LIST 120 + 10 * :n  70 + 14 * :n  40)
  REPEAT 10 [FD :len / 10]
  IF :n = 1 [SETPC HUE 330 + RANDOM 40 SETW 9 DOT DIE]
  RR RANDOM 60
  REPEAT 3 [
    MAKE "a 20 + RANDOM 18
    UP :a
    LAUNCH [BRANCH :len * (0.62 + (RANDOM 12) / 100) :n - 1]
    DOWN :a
    RR 120
  ]
  DIE
END
`},
{id:'fireworks',name:'Fireworks',cam:'orbit',speed:1,src:`; Shells rise from the ground, and each one launches 70
; spark turtles that fly out in random 3D directions and
; fall under gravity.

TO STARTUP
  HT
  REPEAT 9 [LAUNCH [SHELL] WAIT 25 + RANDOM 30]
END

TO SHELL
  LOCAL "h
  MAKE "h RANDOM 360
  PU SETXYZ (RANDOM 500) - 250 -380 (RANDOM 500) - 250 PD
  SETW 1.5 SETPC [120 110 140]
  REPEAT 26 [SETY YCOR + 18 + RANDOM 4]
  REPEAT 70 [LAUNCH [SPARK :h + RANDOM 50]]
  DIE
END

TO SPARK :hue
  LOCAL "vx LOCAL "vy LOCAL "vz LOCAL "th LOCAL "u LOCAL "r LOCAL "c
  MAKE "th RANDOM 360
  MAKE "u (RANDOM 2001) / 1000 - 1
  MAKE "r SQRT 1 - :u * :u
  MAKE "vx 9 * :r * COS :th
  MAKE "vz 9 * :r * SIN :th
  MAKE "vy 9 * :u
  MAKE "c HUE :hue
  SETW 2.5
  REPEAT 45 [
    SETPC (LIST (ITEM 1 :c) * (1 - REPCOUNT / 60)
                (ITEM 2 :c) * (1 - REPCOUNT / 60)
                (ITEM 3 :c) * (1 - REPCOUNT / 60))
    MAKE "vy :vy - 0.32
    MAKE "vx :vx * 0.97 MAKE "vz :vz * 0.97
    SETXYZ XCOR + :vx YCOR + :vy ZCOR + :vz
  ]
  DIE
END
`},
{id:'nautilus',name:'Nautilus',cam:'orbit',speed:30,src:`; A ring perpendicular to the heading, then a short move
; along a curving path, 160 times. The rings grow by 2.4%
; each time, which gives a logarithmic shell.

TO STARTUP
  HT SETW 1.5
  MAKE "s 2
  REPEAT 160 [
    SETPC HUE 20 + REPCOUNT * 2
    RING :s * 6
    PU FD :s * 2.2 RT 11 DOWN 2.5 PD
    MAKE "s :s * 1.024
  ]
END

TO RING :r
  UP 90 PU LT 90 FD :r RT 90 PD
  REPEAT 48 [FD :r * 2 * PI / 48 RT 7.5]
  PU LT 90 BK :r RT 90 DOWN 90 PD
END
`},
{id:'pursuit',name:'Pursuit',cam:'orbit',speed:2,src:`; Four turtles start at the corners of a tetrahedron, and
; each one chases the next. Every few steps a turtle draws a
; line to its target; the lines sweep out a twisted surface.

TO STARTUP
  HT
  LAUNCH [CHASE  300  300  300 0]
  LAUNCH [CHASE -300 -300  300 90]
  LAUNCH [CHASE -300  300 -300 180]
  LAUNCH [CHASE  300 -300 -300 270]
END

TO CHASE :x :y :z :hue
  LOCAL "me LOCAL "t
  PU SETXYZ :x :y :z PD
  WAIT 0
  SETW 3
  REPEAT 700 [
    MAKE "t POSOF 1 + REMAINDER WHO 4
    IF (DISTANCE :t) < 4 [STOP]
    SETPC HUE :hue
    SETW 3
    FACE :t FD 2.2
    IF 0 = REMAINDER REPCOUNT 7 [
      MAKE "me POS
      SETW 0.8 SETPC HUE :hue + 40
      SETPOS :t
      PU SETPOS :me PD
    ]
    WAIT 0
  ]
END
`},
{id:'galaxy',name:'Galaxy',cam:'drift',speed:3,src:`; Twenty-four turtles each roll to their own angle and
; draw a slowly widening spiral that climbs a little on
; every turn.

TO STARTUP
  HT
  REPEAT 24 [LAUNCH [ARM REPCOUNT]]
END

TO ARM :k
  LOCAL "d
  MAKE "d 1
  RR :k * 15
  UP 4
  SETW 1.5
  REPEAT 420 [
    SETPC HUE 180 + REPCOUNT / 2 + :k * 4
    FD :d RT 6 UP 0.25
    MAKE "d :d + 0.035
  ]
END
`},
{id:'lissajous-cage',name:'Lissajous cage',cam:'orbit',speed:8,src:`; Three 3D Lissajous figures: x, y and z are sines with
; different whole-number frequencies, so each curve closes
; after one pass of t from 0 to 360.

TO STARTUP
  HT
  LAUNCH [LISSA 3 4 5 0]
  LAUNCH [LISSA 5 6 7 120]
  LAUNCH [LISSA 2 3 7 240]
END

TO LISSA :a :b :c :hue
  LOCAL "t
  SETW 2.5
  PU SETXYZ 0 300 * SIN 30 0 PD
  FOR [t 0 360 0.2] [
    SETPC HUE :hue + :t
    SETXYZ 300 * SIN :a * :t  300 * SIN :b * :t + 30  300 * SIN :c * :t
  ]
END
`},
{id:'armillary',name:'Armillary',cam:'chase',speed:6,src:`; Rings of circles: one set rolls around the heading, then
; the turtle pitches up 90 degrees and draws a second set
; across the first.

TO STARTUP
  HT SETW 2
  REPEAT 18 [SETPC HUE REPCOUNT * 8 CIRCLE RR 10]
  UP 90
  REPEAT 18 [SETPC HUE 180 + REPCOUNT * 8 CIRCLE RR 10]
END

TO CIRCLE
  REPEAT 60 [FD 16 RT 6]
END
`},
{id:'sierpinski-tetrahedron',name:'Sierpinski tetrahedron',cam:'orbit',speed:3,src:`; A tetrahedron split into four half-size tetrahedra at its
; corners, five levels deep: 1024 small tetrahedra. Four
; turtles each build one corner of the first split.

TO STARTUP
  HT SETW 1.6
  MAKE "s 360
  MAKE "a (LIST 0 :s 0)
  MAKE "b (LIST :s * 0.943  0 - :s / 3  0)
  MAKE "c (LIST 0 - :s * 0.471  0 - :s / 3  :s * 0.816)
  MAKE "d (LIST 0 - :s * 0.471  0 - :s / 3  0 - :s * 0.816)
  LAUNCH [TET :a (MID :a :b) (MID :a :c) (MID :a :d) 4]
  LAUNCH [TET (MID :b :a) :b (MID :b :c) (MID :b :d) 4]
  LAUNCH [TET (MID :c :a) (MID :c :b) :c (MID :c :d) 4]
  LAUNCH [TET (MID :d :a) (MID :d :b) (MID :d :c) :d 4]
END

TO TET :a :b :c :d :n
  IF :n = 0 [EDGES :a :b :c :d STOP]
  TET :a (MID :a :b) (MID :a :c) (MID :a :d) :n - 1
  TET (MID :b :a) :b (MID :b :c) (MID :b :d) :n - 1
  TET (MID :c :a) (MID :c :b) :c (MID :c :d) :n - 1
  TET (MID :d :a) (MID :d :b) (MID :d :c) :d :n - 1
END

TO MID :p :q
  OUTPUT (LIST ((ITEM 1 :p) + (ITEM 1 :q)) / 2
               ((ITEM 2 :p) + (ITEM 2 :q)) / 2
               ((ITEM 3 :p) + (ITEM 3 :q)) / 2)
END

TO EDGES :a :b :c :d
  SETPC HUE 190 + (ITEM 2 :a) * 0.45
  PU SETPOS :a PD SETPOS :b SETPOS :c SETPOS :a SETPOS :d SETPOS :b
  PU SETPOS :c PD SETPOS :d
END
`},
{id:'menger-sponge',name:'Menger sponge',cam:'orbit',speed:12,src:`; Split a cube into 27, keep the 20 that are not on a center
; line, and repeat. Each level is outlined in its own color
; and weight, so you see the 1, 20 and 400 cubes at once.

TO STARTUP
  HT
  MENGER -270 -270 -270 540 2
END

TO MENGER :x :y :z :s :n
  LOCAL "t
  CUBE :x :y :z :s :n
  IF :n = 0 [STOP]
  MAKE "t :s / 3
  FOR [i 0 2] [FOR [j 0 2] [FOR [k 0 2] [
    IF (:i = 1) + (:j = 1) + (:k = 1) < 2 [
      MENGER :x + :i * :t :y + :j * :t :z + :k * :t :t :n - 1
    ]
  ]]]
END

TO CUBE :x :y :z :s :n
  LOCAL "x2 LOCAL "y2 LOCAL "z2
  MAKE "x2 :x + :s MAKE "y2 :y + :s MAKE "z2 :z + :s
  SETW 1 + :n * 1.6
  SETPC HUE 20 + :n * 75
  PU SETXYZ :x :y :z PD
  SETXYZ :x2 :y :z SETXYZ :x2 :y2 :z SETXYZ :x :y2 :z SETXYZ :x :y :z
  SETXYZ :x :y :z2 SETXYZ :x2 :y :z2 SETXYZ :x2 :y2 :z2
  SETXYZ :x :y2 :z2 SETXYZ :x :y :z2
  PU SETXYZ :x2 :y :z PD SETXYZ :x2 :y :z2
  PU SETXYZ :x2 :y2 :z PD SETXYZ :x2 :y2 :z2
  PU SETXYZ :x :y2 :z PD SETXYZ :x :y2 :z2
END
`},
{id:'h-tree',name:'H-tree',cam:'drift',speed:0.3,src:`; A 3D H-tree. Each line launches two turtles, one at each
; end, that pitch and roll 90 degrees so the next line runs
; along the next axis. Lengths shrink by the cube root of 2,
; so every three levels the whole figure repeats at half size.

TO STARTUP
  HT
  HTREE 380 11
END

TO HTREE :len :n
  SETW 0.6 + :n * 0.45
  SETPC HUE 150 + :n * 24
  PU BK :len / 2 PD
  REPEAT 8 [FD :len / 8]
  IF :n = 1 [DIE]
  LAUNCH [UP 90 RR 90 HTREE :len * 0.7937 :n - 1]
  PU BK :len PD
  LAUNCH [UP 90 RR 90 HTREE :len * 0.7937 :n - 1]
  DIE
END
`},
{id:'koch-in-3d',name:'Koch in 3D',cam:'chase',speed:6,src:`; The Koch snowflake, but every sub-curve rolls 90 degrees
; around its own heading first, so each level of bumps
; folds into a plane at right angles to the one before.

TO STARTUP
  HT SETW 1.8
  PU SETXYZ -260 -150 0 PD
  SETH 30
  REPEAT 3 [KOCH 520 5 RT 120]
END

TO KOCH :len :n
  IF :n = 0 [SETPC HUE 200 + XCOR / 2 + ZCOR FD :len STOP]
  RR 90
  KOCH :len / 3 :n - 1
  LT 60
  KOCH :len / 3 :n - 1
  RT 120
  KOCH :len / 3 :n - 1
  LT 60
  KOCH :len / 3 :n - 1
  RL 90
END
`},
{id:'fern-bush',name:'Fern bush',cam:'drift',speed:20,src:`; Barnsley's fern: four affine maps picked at random with
; probabilities 1, 85, 7 and 7 percent. Six turtles each grow
; one frond in its own vertical plane, as a cloud of dots.

TO STARTUP
  HT
  REPEAT 6 [LAUNCH [FROND REPCOUNT * 60 + RANDOM 20]]
END

TO FROND :ang
  LOCAL "x LOCAL "y LOCAL "nx LOCAL "ny LOCAL "r LOCAL "ca LOCAL "sa
  MAKE "x 0 MAKE "y 0
  MAKE "ca COS :ang MAKE "sa SIN :ang
  PU SETW 2.4
  REPEAT 4500 [
    MAKE "r RANDOM 100
    IFELSE :r < 1 [MAKE "nx 0 MAKE "ny 0.16 * :y] [
      IFELSE :r < 86 [
        MAKE "nx 0.85 * :x + 0.04 * :y
        MAKE "ny -0.04 * :x + 0.85 * :y + 1.6
      ] [
        IFELSE :r < 93 [
          MAKE "nx 0.2 * :x - 0.26 * :y
          MAKE "ny 0.23 * :x + 0.22 * :y + 1.6
        ] [
          MAKE "nx -0.15 * :x + 0.28 * :y
          MAKE "ny 0.26 * :x + 0.24 * :y + 0.44
        ]
      ]
    ]
    MAKE "x :nx MAKE "y :ny
    SETXYZ :y * 18 * :ca - :x * 45 * :sa  :y * 54 - 280  :y * 18 * :sa + :x * 45 * :ca
    SETPC HUE 80 + :y * 7
    DOT
  ]
END
`},
];

// ======================= Small 3D math =======================
const V={
  sub:(a,b)=>[a[0]-b[0],a[1]-b[1],a[2]-b[2]], add:(a,b)=>[a[0]+b[0],a[1]+b[1],a[2]+b[2]],
  mul:(a,s)=>[a[0]*s,a[1]*s,a[2]*s], dot:(a,b)=>a[0]*b[0]+a[1]*b[1]+a[2]*b[2],
  cross:(a,b)=>[a[1]*b[2]-a[2]*b[1],a[2]*b[0]-a[0]*b[2],a[0]*b[1]-a[1]*b[0]],
  len:a=>Math.hypot(a[0],a[1],a[2]), norm:a=>{const l=Math.hypot(a[0],a[1],a[2])||1;return[a[0]/l,a[1]/l,a[2]/l]},
  lerp:(a,b,t)=>[a[0]+(b[0]-a[0])*t,a[1]+(b[1]-a[1])*t,a[2]+(b[2]-a[2])*t],
};
function perspective(fovy,asp,n,f){const t=1/Math.tan(fovy/2),nf=1/(n-f);return new Float32Array([t/asp,0,0,0, 0,t,0,0, 0,0,(f+n)*nf,-1, 0,0,2*f*n*nf,0])}
function lookAt(e,c,up){
  const z=V.norm(V.sub(e,c)),x=V.norm(V.cross(up,z)),y=V.cross(z,x);
  return {m:new Float32Array([x[0],y[0],z[0],0, x[1],y[1],z[1],0, x[2],y[2],z[2],0, -V.dot(x,e),-V.dot(y,e),-V.dot(z,e),1]),x,y,z};
}
function mul4(a,b){const o=new Float32Array(16);for(let c=0;c<4;c++)for(let r=0;r<4;r++){let s=0;for(let k=0;k<4;k++)s+=a[k*4+r]*b[c*4+k];o[c*4+r]=s}return o}

// ======================= Looks =======================
const LOOK_DEFS=[
  {id:'neon',name:'Neon',mode:0,gain:1.25,minPx:1.35,wscale:1,bloom:1.0,thresh:.3,aberr:.0035,scan:0,flick:0,grain:.03,vig:.9,dof:2,exposure:1,
   bg0:[.003,.003,.010],bg1:[.018,.010,.040],neb:[.09,.025,.16],nebK:.7,stars:1,fog:[.7,3.2],
   pa:[.5,.5,.5],pb:[.5,.5,.5],pc:[1,1,1],pd:[0,.33,.67],hueRate:.0007,mono:0,tint:[1,1,1],pulse:1},
  {id:'hologram',name:'Hologram',mode:0,gain:1.1,minPx:1.2,wscale:.9,bloom:.85,thresh:.25,aberr:.008,scan:1,flick:1,grain:.05,vig:1.1,dof:1,exposure:1.1,
   bg0:[.0,.008,.012],bg1:[.004,.03,.04],neb:[.0,.08,.09],nebK:.6,stars:.6,fog:[.6,2.6],
   pa:[.4,.7,.8],pb:[.3,.3,.2],pc:[1,1,1],pd:[.1,.15,.2],hueRate:.0004,mono:.85,tint:[.35,.95,1],pulse:1.4},
  {id:'tubes',name:'Tubes',mode:1,gain:1,minPx:1,wscale:2.4,bloom:.35,thresh:.9,aberr:0,scan:0,flick:0,grain:.02,vig:.75,dof:0,exposure:1.05,
   bg0:[.012,.016,.035],bg1:[.06,.07,.12],neb:[.05,.04,.09],nebK:.35,stars:.35,fog:[.9,3.6],
   pa:[.55,.45,.4],pb:[.45,.4,.35],pc:[1,.9,.8],pd:[.05,.2,.35],hueRate:.0005,mono:0,tint:[1,1,1],pulse:.6},
  {id:'dream',name:'Dream',mode:0,gain:1.15,minPx:1.6,wscale:1.3,bloom:1.35,thresh:.2,aberr:.003,scan:0,flick:0,grain:.025,vig:1,dof:7,exposure:1,
   bg0:[.012,.004,.02],bg1:[.05,.02,.06],neb:[.14,.05,.14],nebK:.9,stars:.8,fog:[.8,3.5],
   pa:[.75,.6,.7],pb:[.25,.3,.25],pc:[1,1,1],pd:[.0,.2,.45],hueRate:.0005,mono:.15,tint:[1,.8,.95],pulse:1},
];
const CAM_IDS=['orbit','chase','drift'];

// ======================= WebGL renderer =======================
// One renderer per mount. Throws an Error with .code 'no-webgl2' if the canvas can't give a WebGL 2 context.
// now(): the runtime clock, stamped on each segment for the "just drawn" flash.
function createRenderer(cv,maxSeg,now){
  const STRIDE=36;let MAXSEG=Math.max(1024,maxSeg|0);
  let gl,canvas,W=1,H=1,hdr=false,samples=0;
  let cap=Math.min(1<<16,MAXSEG),count=0,uploaded=0,gpuCap=0;
  let buf=new ArrayBuffer(cap*STRIDE),f32=new Float32Array(buf),u8=new Uint8Array(buf);
  const bb=[Infinity,Infinity,Infinity,-Infinity,-Infinity,-Infinity];
  let segProg,quadVbo,instVbo,vao,fsVao,P={},msFbo,msColor,msDepth,sceneFbo,sceneTex,bloom=[];
  const api={full:false,get count(){return count},bb};

  function grow(){
    const ncap=Math.min(cap*2,MAXSEG),nb=new ArrayBuffer(ncap*STRIDE);
    new Uint8Array(nb).set(u8.subarray(0,count*STRIDE));
    buf=nb;f32=new Float32Array(buf);u8=new Uint8Array(buf);cap=ncap;
  }
  api.seg=(x0,y0,z0,x1,y1,z1,c,w)=>{
    if(count>=cap){if(cap>=MAXSEG){api.full=true;return}grow()}
    const o=count*9;
    f32[o]=x0;f32[o+1]=y0;f32[o+2]=z0;f32[o+3]=x1;f32[o+4]=y1;f32[o+5]=z1;f32[o+6]=+w;f32[o+7]=now();
    const b=o*4+32;
    u8[b]=Math.max(0,Math.min(255,c[0]*255));u8[b+1]=Math.max(0,Math.min(255,c[1]*255));u8[b+2]=Math.max(0,Math.min(255,c[2]*255));u8[b+3]=Math.max(0,Math.min(255,(c[3]??1)*255));
    if(x0<bb[0])bb[0]=x0;if(y0<bb[1])bb[1]=y0;if(z0<bb[2])bb[2]=z0;if(x0>bb[3])bb[3]=x0;if(y0>bb[4])bb[4]=y0;if(z0>bb[5])bb[5]=z0;
    if(x1<bb[0])bb[0]=x1;if(y1<bb[1])bb[1]=y1;if(z1<bb[2])bb[2]=z1;if(x1>bb[3])bb[3]=x1;if(y1>bb[4])bb[4]=y1;if(z1>bb[5])bb[5]=z1;
    count++;
  };
  api.clear=()=>{
    // Shrink back if the cap was lowered or a big program grew the buffer past what's now allowed.
    if(cap>MAXSEG){cap=Math.min(1<<16,MAXSEG);buf=new ArrayBuffer(cap*STRIDE);f32=new Float32Array(buf);u8=new Uint8Array(buf)}
    count=0;uploaded=0;api.full=false;bb[0]=bb[1]=bb[2]=Infinity;bb[3]=bb[4]=bb[5]=-Infinity};

  const FSV=`#version 300 es
const vec2 P[3]=vec2[3](vec2(-1.,-1.),vec2(3.,-1.),vec2(-1.,3.));out vec2 uv;
void main(){vec2 p=P[gl_VertexID];uv=p*.5+.5;gl_Position=vec4(p,0.,1.);}`;
  const SEGV=`#version 300 es
precision highp float;
layout(location=0) in vec2 aC;
layout(location=1) in vec3 aP0;
layout(location=2) in vec3 aP1;
layout(location=3) in vec2 aWB;
layout(location=4) in vec4 aCol;
uniform mat4 uVP,uView;uniform vec3 uEye;
uniform float uPxK,uMinPx,uWScale,uFocus,uDof,uTime,uCount,uDissolve,uPulse,uWave,uGain,uHueRate,uHueShift,uMono;
uniform vec3 uPA,uPB,uPC,uPD,uTint;
out vec2 vQ;flat out float vL;out vec4 vC;out float vZ;flat out vec3 vSide,vCap,vE;
float hash(float n){return fract(sin(n*12.9898+4.1414)*43758.5453);}
void main(){
  float id=float(gl_InstanceID);
  float h=hash(id);
  if(h<uDissolve){gl_Position=vec4(2.,2.,2.,1.);vC=vec4(0.);return;}
  vec3 p0=aP0,p1=aP1,mid=.5*(p0+p1),ax=p1-p0;float len=length(ax);
  vec3 E=normalize(uEye-mid);
  vec3 dir=len>1e-5?ax/len:vec3(0.,1.,0.);
  vec3 side=cross(dir,E);float sl=length(side);
  side=sl>1e-4?side/sl:normalize(cross(vec3(.12,1.,.07),E));
  vec3 cap=cross(E,side);if(dot(cap,dir)<0.)cap=-cap;
  float z=max(-(uView*vec4(mid,1.)).z,.5);
  float pw=z*uPxK;
  float r=max(aWB.x*.5*uWScale,.05),rpx=r/pw;
  float coc=uDof*abs(z-uFocus)/max(uFocus,1.);
  float tpx=max(rpx,uMinPx)+min(coc,uDof*2.5);
  float a=clamp(max(rpx,.4)/tpx,.03,1.);
  float Rw=tpx*pw,lp=max(dot(ax,cap),0.);
  vec3 p=(aC.x<.5?p0-cap*Rw:p1+cap*Rw)+side*Rw*aC.y;
  vQ=vec2(aC.x<.5?-1.:lp/Rw+1.,aC.y);vL=lp/Rw;
  vec3 c=aCol.rgb;
  float isAuto=step(max(c.r,max(c.g,c.b)),.06);
  vec3 pal=uPA+uPB*cos(6.28318*(uPC*(id*uHueRate+uHueShift)+uPD));
  c=pow(max(mix(c,pal,isAuto),0.),vec3(2.2));
  float lum=dot(c,vec3(.3,.59,.11));
  c=mix(c,uTint*lum*1.3,uMono);
  float age=uTime-aWB.y;
  float boost=1.+uPulse*exp(-age*2.2)*2.5;
  float ph=fract(id/max(uCount,1.)-uTime*.05);
  boost+=exp(-pow((ph-.5)*28.,2.))*uWave*2.5;
  boost+=(1.-smoothstep(uDissolve,uDissolve+.05,h))*step(.001,uDissolve)*5.;
  vC=vec4(c*boost*uGain,a*aCol.a);vZ=z;vSide=side;vCap=cap;vE=E;
  gl_Position=uVP*vec4(p,1.);
}`;
  const SEGF=`#version 300 es
precision highp float;
in vec2 vQ;flat in float vL;in vec4 vC;in float vZ;flat in vec3 vSide,vCap,vE;
uniform int uMode;uniform float uFogNear,uFogFar;uniform vec3 uLight,uFogCol;
out vec4 o;
void main(){
  float dx=vQ.x<0.?vQ.x:(vQ.x>vL?vQ.x-vL:0.);
  float d=length(vec2(dx,vQ.y));
  if(d>1.)discard;
  float aw=fwidth(d)*1.2+1e-4;
  float cov=1.-smoothstep(1.-aw,1.,d);
  float fog=1.-smoothstep(uFogNear,uFogFar,vZ);
  float alpha=vC.a*cov;
  if(uMode==0){
    float core=exp(-d*d*3.5)*.75+.25*(1.-d*d);
    o=vec4(vC.rgb*core*(.25+.75*fog)*alpha,0.);
  }else{
    float nz=sqrt(max(1.-d*d,0.));
    vec3 N=normalize(vSide*vQ.y+vCap*dx+vE*nz);
    float dif=max(dot(N,uLight),0.);
    float spec=pow(max(dot(N,normalize(uLight+vE)),0.),40.);
    float rim=pow(1.-nz,2.5);
    vec3 col=vC.rgb*(.12+.95*dif)+vec3(.9)*spec+vC.rgb*rim*.5;
    o=vec4(mix(uFogCol,col,fog),alpha);
  }
}`;
  const PRE=`#version 300 es
precision highp float;in vec2 uv;uniform sampler2D uSrc;uniform vec2 uTx;uniform float uThresh;out vec4 o;
void main(){vec3 c=texture(uSrc,uv).rgb*4.;
c+=texture(uSrc,uv+uTx*vec2(-1,-1)).rgb+texture(uSrc,uv+uTx*vec2(1,-1)).rgb+texture(uSrc,uv+uTx*vec2(-1,1)).rgb+texture(uSrc,uv+uTx).rgb;c/=8.;
float b=max(c.r,max(c.g,c.b));o=vec4(c*max(b-uThresh,0.)/max(b,1e-4),1.);}`;
  const DOWN=`#version 300 es
precision highp float;in vec2 uv;uniform sampler2D uSrc;uniform vec2 uTx;out vec4 o;
void main(){vec3 c=texture(uSrc,uv).rgb*4.;
c+=texture(uSrc,uv+uTx*vec2(-1,-1)).rgb+texture(uSrc,uv+uTx*vec2(1,-1)).rgb+texture(uSrc,uv+uTx*vec2(-1,1)).rgb+texture(uSrc,uv+uTx).rgb;o=vec4(c/8.,1.);}`;
  const UP=`#version 300 es
precision highp float;in vec2 uv;uniform sampler2D uSrc;uniform vec2 uTx;out vec4 o;
void main(){vec2 h=uTx;vec3 c=
texture(uSrc,uv+vec2(-2.*h.x,0.)).rgb+texture(uSrc,uv+vec2(2.*h.x,0.)).rgb+texture(uSrc,uv+vec2(0.,2.*h.y)).rgb+texture(uSrc,uv+vec2(0.,-2.*h.y)).rgb+
2.*(texture(uSrc,uv+vec2(-h.x,h.y)).rgb+texture(uSrc,uv+h).rgb+texture(uSrc,uv+vec2(h.x,-h.y)).rgb+texture(uSrc,uv-h).rgb);o=vec4(c/12.,1.);}`;
  const COMP=`#version 300 es
precision highp float;in vec2 uv;
uniform sampler2D uScene,uBloom;uniform vec2 uRes;uniform float uTime,uBloomK,uAberr,uScan,uFlick,uGrain,uVig,uFade,uExposure,uStars,uNebK,uTanH;
uniform vec3 uBg0,uBg1,uNeb;uniform mat3 uRot;out vec4 o;
float h3(vec3 p){p=fract(p*vec3(443.897,441.423,437.195));p+=dot(p,p.yzx+19.19);return fract((p.x+p.y)*p.z);}
float n3(vec3 p){vec3 i=floor(p),f=fract(p);f=f*f*(3.-2.*f);
  return mix(mix(mix(h3(i),h3(i+vec3(1,0,0)),f.x),mix(h3(i+vec3(0,1,0)),h3(i+vec3(1,1,0)),f.x),f.y),
             mix(mix(h3(i+vec3(0,0,1)),h3(i+vec3(1,0,1)),f.x),mix(h3(i+vec3(0,1,1)),h3(i+vec3(1,1,1)),f.x),f.y),f.z);}
float fbm(vec3 p){float s=0.,a=.5;for(int i=0;i<5;i++){s+=a*n3(p);p=p*2.03+vec3(1.7,9.2,3.1);a*=.5;}return s;}
void main(){
  vec2 ndc=uv*2.-1.;float asp=uRes.x/uRes.y;
  vec3 ray=normalize(uRot*vec3(ndc.x*uTanH*asp,ndc.y*uTanH,-1.));
  vec3 bg=mix(uBg0,uBg1,.5+.5*ray.y);
  float n=fbm(ray*2.4+vec3(0.,0.,uTime*.008));
  bg+=uNeb*pow(n,3.)*uNebK*2.;
  vec3 sp=ray*170.,cell=floor(sp),f=fract(sp)-.5;float hs=h3(cell);
  if(hs>.984){vec3 off=vec3(h3(cell+1.),h3(cell+2.),h3(cell+3.))-.5;float dd=length(f-off*.6);
    float tw=.55+.45*sin(uTime*(1.+hs*4.)+hs*91.);bg+=vec3(.9,.92,1.)*smoothstep(.16,0.,dd)*tw*uStars*(hs-.984)*90.;}
  vec2 dc=uv-.5;vec4 sc;
  if(uAberr>0.){vec2 of=dc*uAberr*2.;sc=texture(uScene,uv);sc.r=texture(uScene,uv+of).r;sc.b=texture(uScene,uv-of).b;}else sc=texture(uScene,uv);
  vec3 col=bg*(1.-clamp(sc.a,0.,1.))+sc.rgb+texture(uBloom,uv).rgb*uBloomK;
  if(uScan>0.){
    col*=1.-uScan*.28*(.5+.5*sin(gl_FragCoord.y*2.0944));
    float band=exp(-pow((fract(uv.y*.7-uTime*.09)-.5)*10.,2.));col*=1.+uScan*band*.35;
  }
  col*=1.-uFlick*.05*step(.93,h3(vec3(floor(uTime*24.),1.,2.)));
  col*=uExposure;
  col=(col*(2.51*col+.03))/(col*(2.43*col+.59)+.14);
  col=pow(clamp(col,0.,1.),vec3(1./2.2));
  col*=1.-uVig*dot(dc,dc)*1.5;
  col+=(h3(vec3(gl_FragCoord.xy,fract(uTime*7.)*100.))-.5)*uGrain;
  o=vec4(col*uFade,1.);
}`;
  function sh(t,s){const x=gl.createShader(t);gl.shaderSource(x,s);gl.compileShader(x);if(!gl.getShaderParameter(x,gl.COMPILE_STATUS))throw new Error(gl.getShaderInfoLog(x));return x}
  function pg(v,f){const p=gl.createProgram();gl.attachShader(p,sh(gl.VERTEX_SHADER,v));gl.attachShader(p,sh(gl.FRAGMENT_SHADER,f));gl.linkProgram(p);if(!gl.getProgramParameter(p,gl.LINK_STATUS))throw new Error(gl.getProgramInfoLog(p));
    const u={};const n=gl.getProgramParameter(p,gl.ACTIVE_UNIFORMS);for(let i=0;i<n;i++){const nm=gl.getActiveUniform(p,i).name;u[nm]=gl.getUniformLocation(p,nm)}return{p,u}}

  function init(){
    canvas=cv;gl=cv.getContext('webgl2',{antialias:false,alpha:false,premultipliedAlpha:false,powerPreference:'high-performance'});
    if(!gl){const e=new Error('WebGL 2 is not available');e.code='no-webgl2';throw e}
    hdr=!!gl.getExtension('EXT_color_buffer_float');
    samples=Math.min(4,gl.getParameter(gl.MAX_SAMPLES)||0);
    segProg=pg(SEGV,SEGF);P.pre=pg(FSV,PRE);P.down=pg(FSV,DOWN);P.up=pg(FSV,UP);P.comp=pg(FSV,COMP);
    vao=gl.createVertexArray();gl.bindVertexArray(vao);
    quadVbo=gl.createBuffer();gl.bindBuffer(gl.ARRAY_BUFFER,quadVbo);
    gl.bufferData(gl.ARRAY_BUFFER,new Float32Array([0,-1, 1,-1, 0,1, 1,1]),gl.STATIC_DRAW);
    gl.enableVertexAttribArray(0);gl.vertexAttribPointer(0,2,gl.FLOAT,false,0,0);
    instVbo=gl.createBuffer();bindInst();
    gl.bindVertexArray(null);
    fsVao=gl.createVertexArray();
  }
  function bindInst(){
    gl.bindVertexArray(vao);gl.bindBuffer(gl.ARRAY_BUFFER,instVbo);
    const a=(loc,n,type,norm,off)=>{gl.enableVertexAttribArray(loc);gl.vertexAttribPointer(loc,n,type,norm,STRIDE,off);gl.vertexAttribDivisor(loc,1)};
    a(1,3,gl.FLOAT,false,0);a(2,3,gl.FLOAT,false,12);a(3,2,gl.FLOAT,false,24);a(4,4,gl.UNSIGNED_BYTE,true,32);
  }
  function upload(){
    if(uploaded>count)uploaded=0;
    if(uploaded===count)return;
    gl.bindBuffer(gl.ARRAY_BUFFER,instVbo);
    if(gpuCap<cap){gpuCap=cap;gl.bufferData(gl.ARRAY_BUFFER,gpuCap*STRIDE,gl.DYNAMIC_DRAW);uploaded=0}
    gl.bufferSubData(gl.ARRAY_BUFFER,uploaded*STRIDE,u8,uploaded*STRIDE,(count-uploaded)*STRIDE);
    uploaded=count;
  }
  function tex(w,h){
    const t=gl.createTexture();gl.bindTexture(gl.TEXTURE_2D,t);
    gl.texImage2D(gl.TEXTURE_2D,0,hdr?gl.RGBA16F:gl.RGBA8,w,h,0,gl.RGBA,hdr?gl.HALF_FLOAT:gl.UNSIGNED_BYTE,null);
    gl.texParameteri(gl.TEXTURE_2D,gl.TEXTURE_MIN_FILTER,gl.LINEAR);gl.texParameteri(gl.TEXTURE_2D,gl.TEXTURE_MAG_FILTER,gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D,gl.TEXTURE_WRAP_S,gl.CLAMP_TO_EDGE);gl.texParameteri(gl.TEXTURE_2D,gl.TEXTURE_WRAP_T,gl.CLAMP_TO_EDGE);
    const f=gl.createFramebuffer();gl.bindFramebuffer(gl.FRAMEBUFFER,f);gl.framebufferTexture2D(gl.FRAMEBUFFER,gl.COLOR_ATTACHMENT0,gl.TEXTURE_2D,t,0);
    return{t,f,w,h};
  }
  function targets(){
    for(const x of [sceneTex,...bloom])if(x){gl.deleteTexture(x.t);gl.deleteFramebuffer(x.f)}
    if(msFbo){gl.deleteFramebuffer(msFbo);gl.deleteRenderbuffer(msColor);gl.deleteRenderbuffer(msDepth)}
    sceneTex=tex(W,H);
    msFbo=gl.createFramebuffer();gl.bindFramebuffer(gl.FRAMEBUFFER,msFbo);
    msColor=gl.createRenderbuffer();gl.bindRenderbuffer(gl.RENDERBUFFER,msColor);
    gl.renderbufferStorageMultisample(gl.RENDERBUFFER,samples,hdr?gl.RGBA16F:gl.RGBA8,W,H);
    gl.framebufferRenderbuffer(gl.FRAMEBUFFER,gl.COLOR_ATTACHMENT0,gl.RENDERBUFFER,msColor);
    msDepth=gl.createRenderbuffer();gl.bindRenderbuffer(gl.RENDERBUFFER,msDepth);
    gl.renderbufferStorageMultisample(gl.RENDERBUFFER,samples,gl.DEPTH_COMPONENT24,W,H);
    gl.framebufferRenderbuffer(gl.FRAMEBUFFER,gl.DEPTH_ATTACHMENT,gl.RENDERBUFFER,msDepth);
    bloom=[];let w=W,h=H;
    for(let i=0;i<6;i++){w=Math.max(1,w>>1);h=Math.max(1,h>>1);bloom.push(tex(w,h))}
  }
  api.resize=(w,h)=>{if(w===W&&h===H&&sceneTex)return;W=w;H=h;canvas.width=w;canvas.height=h;targets()};
  function fs(prog,dst,src,setup){
    gl.bindFramebuffer(gl.FRAMEBUFFER,dst?dst.f:null);gl.viewport(0,0,dst?dst.w:W,dst?dst.h:H);
    gl.useProgram(prog.p);gl.activeTexture(gl.TEXTURE0);gl.bindTexture(gl.TEXTURE_2D,src.t);gl.uniform1i(prog.u.uSrc,0);
    if(prog.u.uTx)gl.uniform2f(prog.u.uTx,1/src.w,1/src.h);
    if(setup)setup(prog.u);
    gl.bindVertexArray(fsVao);gl.drawArrays(gl.TRIANGLES,0,3);
  }
  // f: frame state {eye,target,up,fov,near,far,focus,look,time,dissolve,wave,fade,hueShift,camDist}
  api.render=f=>{
    upload();
    const L=f.look,asp=W/H;
    const proj=perspective(f.fov,asp,f.near,f.far),view=lookAt(f.eye,f.target,f.up),vp=mul4(proj,view.m);
    gl.bindFramebuffer(gl.FRAMEBUFFER,msFbo);gl.viewport(0,0,W,H);
    gl.clearColor(0,0,0,0);gl.clearDepth(1);gl.clear(gl.COLOR_BUFFER_BIT|gl.DEPTH_BUFFER_BIT);
    if(count){
      const u=segProg.u;gl.useProgram(segProg.p);
      gl.uniformMatrix4fv(u.uVP,false,vp);gl.uniformMatrix4fv(u.uView,false,view.m);gl.uniform3fv(u.uEye,f.eye);
      gl.uniform1f(u.uPxK,2/(proj[5]*H));gl.uniform1f(u.uMinPx,L.minPx*Math.max(1,H/1100));gl.uniform1f(u.uWScale,L.wscale);
      gl.uniform1f(u.uFocus,f.focus);gl.uniform1f(u.uDof,L.dof*H/1000);gl.uniform1f(u.uTime,f.time);gl.uniform1f(u.uCount,count);
      gl.uniform1f(u.uDissolve,f.dissolve);gl.uniform1f(u.uPulse,L.pulse);gl.uniform1f(u.uWave,f.wave);gl.uniform1f(u.uGain,L.gain*(L.mode===0?Math.max(.3,Math.min(1,Math.sqrt(9000/count))):1));
      gl.uniform1f(u.uHueRate,L.hueRate*Math.max(.25,Math.min(1,6000/Math.max(count,1))));gl.uniform1f(u.uHueShift,f.hueShift);gl.uniform1f(u.uMono,L.mono);
      gl.uniform3fv(u.uPA,L.pa);gl.uniform3fv(u.uPB,L.pb);gl.uniform3fv(u.uPC,L.pc);gl.uniform3fv(u.uPD,L.pd);gl.uniform3fv(u.uTint,L.tint);
      gl.uniform1i(u.uMode,L.mode);gl.uniform1f(u.uFogNear,f.camDist*L.fog[0]);gl.uniform1f(u.uFogFar,f.camDist*L.fog[1]);
      gl.uniform3fv(u.uLight,V.norm(V.add(V.add(V.mul(view.x,-.45),V.mul(view.y,.75)),V.mul(view.z,.5))));
      gl.uniform3fv(u.uFogCol,L.bg1.map(v=>v*1.5));
      if(L.mode===0){gl.disable(gl.DEPTH_TEST);gl.depthMask(false);gl.enable(gl.BLEND);gl.blendFunc(gl.ONE,gl.ONE);gl.disable(gl.SAMPLE_ALPHA_TO_COVERAGE)}
      else{gl.enable(gl.DEPTH_TEST);gl.depthFunc(gl.LEQUAL);gl.depthMask(true);gl.disable(gl.BLEND);if(samples)gl.enable(gl.SAMPLE_ALPHA_TO_COVERAGE);else{gl.enable(gl.BLEND);gl.blendFunc(gl.SRC_ALPHA,gl.ONE_MINUS_SRC_ALPHA)}}
      gl.bindVertexArray(vao);gl.drawArraysInstanced(gl.TRIANGLE_STRIP,0,4,count);
      gl.disable(gl.DEPTH_TEST);gl.depthMask(true);gl.disable(gl.BLEND);gl.disable(gl.SAMPLE_ALPHA_TO_COVERAGE);
    }
    gl.bindFramebuffer(gl.READ_FRAMEBUFFER,msFbo);gl.bindFramebuffer(gl.DRAW_FRAMEBUFFER,sceneTex.f);
    gl.blitFramebuffer(0,0,W,H,0,0,W,H,gl.COLOR_BUFFER_BIT,gl.NEAREST);
    // bloom: threshold + dual-filter down/up chain
    fs(P.pre,bloom[0],sceneTex,u=>gl.uniform1f(u.uThresh,L.thresh));
    for(let i=1;i<bloom.length;i++)fs(P.down,bloom[i],bloom[i-1]);
    gl.enable(gl.BLEND);gl.blendFunc(gl.ONE,gl.ONE);
    for(let i=bloom.length-1;i>0;i--)fs(P.up,bloom[i-1],bloom[i]);
    gl.disable(gl.BLEND);
    const c=P.comp,u=c.u;
    gl.bindFramebuffer(gl.FRAMEBUFFER,null);gl.viewport(0,0,W,H);gl.useProgram(c.p);
    gl.activeTexture(gl.TEXTURE0);gl.bindTexture(gl.TEXTURE_2D,sceneTex.t);gl.uniform1i(u.uScene,0);
    gl.activeTexture(gl.TEXTURE1);gl.bindTexture(gl.TEXTURE_2D,bloom[0].t);gl.uniform1i(u.uBloom,1);
    gl.uniform2f(u.uRes,W,H);gl.uniform1f(u.uTime,f.time);gl.uniform1f(u.uBloomK,L.bloom*(hdr?1:.6));gl.uniform1f(u.uAberr,L.aberr);
    gl.uniform1f(u.uScan,L.scan);gl.uniform1f(u.uFlick,f.reduced?0:L.flick);gl.uniform1f(u.uGrain,f.reduced?L.grain*.4:L.grain);gl.uniform1f(u.uVig,L.vig);
    gl.uniform1f(u.uFade,f.fade);gl.uniform1f(u.uExposure,L.exposure);gl.uniform1f(u.uStars,L.stars);gl.uniform1f(u.uNebK,L.nebK);
    gl.uniform1f(u.uTanH,Math.tan(f.fov/2));gl.uniform3fv(u.uBg0,L.bg0);gl.uniform3fv(u.uBg1,L.bg1);gl.uniform3fv(u.uNeb,L.neb);
    gl.uniformMatrix3fv(u.uRot,false,new Float32Array([...view.x,...view.y,...view.z]));
    gl.bindVertexArray(fsVao);gl.drawArrays(gl.TRIANGLES,0,3);
  };
  // Lowering the cap takes effect when the buffer is next cleared (the next program).
  api.setMaxSegments=n=>{MAXSEG=Math.max(1024,n|0)};
  api.dispose=()=>{
    if(!gl)return;
    if(!gl.isContextLost()){
      for(const x of [sceneTex,...bloom])if(x){gl.deleteTexture(x.t);gl.deleteFramebuffer(x.f)}
      if(msFbo){gl.deleteFramebuffer(msFbo);gl.deleteRenderbuffer(msColor);gl.deleteRenderbuffer(msDepth)}
      for(const p of [segProg,P.pre,P.down,P.up,P.comp])if(p){for(const s of gl.getAttachedShaders(p.p)||[])gl.deleteShader(s);gl.deleteProgram(p.p)}
      gl.deleteBuffer(quadVbo);gl.deleteBuffer(instVbo);gl.deleteVertexArray(vao);gl.deleteVertexArray(fsVao);
      gl.getExtension('WEBGL_lose_context')?.loseContext();
    }
    gl=null;canvas=null;sceneTex=null;bloom=[];msFbo=null;P={};segProg=null;
    buf=null;f32=null;u8=null;count=0;
  };
  init();
  return api;
}

// ======================= Camera =======================
const FOV=48*Math.PI/180;
function createCamera(GL){
  const cam={eye:[0,0,1400],target:[0,0,0],up:[0,1,0],sEye:[0,0,1400],sTarget:[0,0,0],sUp:[0,1,0],
    yaw:0,pitch:.18,zoom:1,manualUntil:0,radius:300,center:[0,0,0],mode:'orbit',chaseOff:null,lastMode:undefined};
  function sceneFit(){
    const b=GL.bb;
    if(!isFinite(b[0]))return{c:[0,0,0],r:250};
    const c=[(b[0]+b[3])/2,(b[1]+b[4])/2,(b[2]+b[5])/2];
    return{c,r:Math.max(60,Math.hypot(b[3]-b[0],b[4]-b[1],b[5]-b[2])/2)};
  }
  function enterOrbit(){
    const d=V.sub(cam.sEye,cam.sTarget),l=V.len(d)||1;
    cam.yaw=Math.atan2(d[0],d[2]);cam.pitch=Math.asin(Math.max(-.95,Math.min(.95,d[1]/l)));
  }
  function update(dt,t,chaseT,reduced){
    const fit=sceneFit();
    cam.center=V.lerp(cam.center,fit.c,1-Math.exp(-dt*1.2));
    cam.radius+=(fit.r-cam.radius)*(1-Math.exp(-dt*1.2));
    const fitDist=cam.radius/Math.sin(FOV/2)*1.05;
    const manual=performance.now()<cam.manualUntil;
    const spin=(reduced?.025:.11)*(manual?0:1);
    let mode=cam.mode;
    if(mode==='chase'&&!chaseT)mode='orbit';
    if(mode!==cam.lastMode){if(mode!=='chase')enterOrbit();cam.lastMode=mode}
    let eye,target,up=[0,1,0],k=1.6;
    if(mode==='chase'){
      const T=chaseT,H=[T.hx,T.hy,T.hz],U=[T.ux,T.uy,T.uz],P=[T.x,T.y,T.z];
      const back=Math.max(70,Math.min(380,cam.radius*.55))*cam.zoom;
      target=V.add(P,V.mul(H,back*.25));
      eye=V.add(V.add(P,V.mul(H,-back)),V.mul(U,back*.45));
      eye=V.add(eye,V.mul(V.norm(V.sub(eye,cam.center)),back*.35));
      up=V.norm(V.add(V.mul(U,.35),[0,.65,0]));
      k=1.1;
    }else{
      cam.yaw+=dt*spin;
      const breathe=1+.12*Math.sin(t*.05);
      let dist=fitDist*cam.zoom*breathe,pitchT=cam.pitch;
      if(!manual)pitchT=.22+.38*Math.sin(t*.043);
      if(mode==='drift'){
        dist*=.72;
        const w=cam.radius*.35;
        target=V.add(cam.center,[w*Math.sin(t*.13),w*.6*Math.sin(t*.17+1),w*Math.sin(t*.11+2)]);
      }else target=cam.center;
      if(!manual)cam.pitch+=(pitchT-cam.pitch)*(1-Math.exp(-dt*.5));
      const cp=Math.cos(cam.pitch);
      eye=V.add(target,V.mul([Math.sin(cam.yaw)*cp,Math.sin(cam.pitch),Math.cos(cam.yaw)*cp],dist));
      k=manual?8:1.6;
    }
    const a=1-Math.exp(-dt*k);
    cam.sEye=V.lerp(cam.sEye,eye,a);cam.sTarget=V.lerp(cam.sTarget,target,a);cam.sUp=V.norm(V.lerp(cam.sUp,up,a));
    return mode;
  }
  return {cam,enterOrbit,update};
}

// ======================= Scoped styles + HUD markup =======================
// Everything is under .lw3d / .lw3d-* so a mount never styles the host page. Font families are named but never loaded here:
// the host page loads them (the standalone page uses Google Fonts) or the fallbacks apply.
const CSS=`
.lw3d{--void:#05060c;--glass:rgba(14,16,30,.62);--edge:rgba(150,165,255,.18);
  --ink:#e6e8f5;--muted:#8d93b3;--accent:#8399ff;--hot:#ff8fbf;--err:#ff8f86;
  --font-display:'Silkscreen',ui-monospace,monospace;
  --font-ui:'IBM Plex Mono',ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;
  position:relative;width:100%;height:100%;overflow:hidden;container-type:size;
  background:var(--void);color:var(--ink);font:13px/1.45 var(--font-ui);color-scheme:dark;text-align:left}
.lw3d [hidden]{display:none!important}
.lw3d-cv{position:absolute;inset:0;width:100%;height:100%;display:block;touch-action:none;cursor:grab}
.lw3d-cv:active{cursor:grabbing}
.lw3d-idle,.lw3d-idle .lw3d-cv{cursor:none}
.lw3d-hud{position:absolute;inset:0;pointer-events:none;transition:opacity .8s ease}
.lw3d-idle .lw3d-hud{opacity:0}
.lw3d-hud>*{pointer-events:auto}
.lw3d-mark{position:absolute;top:calc(14px + env(safe-area-inset-top,0px));left:calc(16px + env(safe-area-inset-left,0px));
  font-family:var(--font-display);font-size:15px;letter-spacing:.04em;display:flex;align-items:center;gap:10px;color:var(--ink);text-shadow:0 0 12px rgba(131,153,255,.5)}
.lw3d-mark small{font-family:var(--font-ui);font-size:11px;color:var(--muted);letter-spacing:.08em;text-transform:uppercase;text-shadow:none}
.lw3d-bar{position:absolute;left:0;right:0;bottom:0;display:flex;flex-wrap:wrap;align-items:flex-end;justify-content:space-between;gap:12px 20px;
  padding:16px max(16px,env(safe-area-inset-right,0px)) calc(16px + env(safe-area-inset-bottom,0px)) max(16px,env(safe-area-inset-left,0px));
  background:linear-gradient(to top,rgba(5,6,12,.75),rgba(5,6,12,0));pointer-events:none}
.lw3d-bar>*{pointer-events:auto}
.lw3d-now{min-width:0}
.lw3d-eyebrow{font-size:11px;letter-spacing:.12em;text-transform:uppercase;color:var(--muted);font-variant-numeric:tabular-nums}
.lw3d-now h1{margin:2px 0 4px;font-family:var(--font-display);font-weight:400;font-size:clamp(22px,4cqw,34px);line-height:1.05;letter-spacing:.02em;text-wrap:balance;color:inherit}
.lw3d-stats{display:flex;flex-wrap:wrap;gap:4px 14px;font-size:11px;color:var(--muted);font-variant-numeric:tabular-nums}
.lw3d-stats b{color:var(--ink);font-weight:500}
.lw3d-stats .lw3d-live{color:var(--hot)}
.lw3d-ctrls{display:flex;flex-wrap:wrap;gap:6px;align-items:center}
.lw3d-ctrls button,.lw3d-panel button{font:inherit;font-size:12px;color:var(--ink);background:var(--glass);border:1px solid var(--edge);border-radius:6px;
  padding:7px 11px;margin:0;cursor:pointer;backdrop-filter:blur(8px);-webkit-backdrop-filter:blur(8px);display:inline-flex;gap:6px;align-items:baseline}
.lw3d-ctrls button:hover,.lw3d-panel button:hover{border-color:var(--accent)}
.lw3d-ctrls button:focus-visible,.lw3d-panel button:focus-visible,.lw3d-panel textarea:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
.lw3d-ctrls button span{color:var(--muted);font-size:10px;text-transform:uppercase;letter-spacing:.08em}
.lw3d-ctrls button[aria-pressed="true"]{border-color:var(--hot);color:var(--hot)}
.lw3d kbd{font:inherit;font-size:10px;color:var(--muted);border:1px solid var(--edge);border-radius:3px;padding:0 4px}
.lw3d-panel{position:absolute;top:0;right:0;bottom:0;width:min(460px,100%);box-sizing:border-box;display:flex;flex-direction:column;gap:10px;
  padding:calc(14px + env(safe-area-inset-top,0px)) max(16px,env(safe-area-inset-right,0px)) calc(14px + env(safe-area-inset-bottom,0px)) 16px;
  background:rgba(9,10,20,.86);border-left:1px solid var(--edge);backdrop-filter:blur(14px);-webkit-backdrop-filter:blur(14px);z-index:5}
.lw3d-panel-h{display:flex;align-items:center;justify-content:space-between;gap:10px}
.lw3d-panel-h h2{margin:0;font-family:var(--font-display);font-weight:400;font-size:13px;letter-spacing:.08em;text-transform:uppercase;color:inherit}
.lw3d-panel textarea{flex:1;min-height:180px;margin:0;resize:none;background:rgba(0,0,0,.35);color:var(--ink);border:1px solid var(--edge);border-radius:6px;
  padding:10px;font:12.5px/1.5 var(--font-ui);tab-size:2;white-space:pre;overflow:auto}
.lw3d-panel .lw3d-row{display:flex;gap:8px;align-items:center;flex-wrap:wrap}
.lw3d-panel .lw3d-go{background:var(--accent);color:#0c0f1c;border-color:var(--accent);font-weight:600}
.lw3d-msg{font-size:12px;color:var(--muted);min-height:1.4em}
.lw3d-msg.lw3d-err{color:var(--err)}
.lw3d-ref{font-size:11.5px;color:var(--muted);border-top:1px solid var(--edge);padding-top:8px;display:grid;grid-template-columns:auto 1fr;gap:3px 12px}
.lw3d-ref code{color:var(--ink);white-space:nowrap;font:inherit}
@container (max-width:560px){.lw3d-ctrls button span{display:none}.lw3d-mark small{display:none}}
@media (prefers-reduced-motion:reduce){.lw3d-hud{transition:none}}
`;
const HUD_HTML=`
<div class="lw3d-mark"><svg width="18" height="18" viewBox="0 0 18 18" aria-hidden="true"><path d="M9 1 17 15H1Z" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="M9 6v9" stroke="var(--hot)" stroke-width="1.6"/></svg>LogoWriter GL<small>3D · screensaver</small></div>
<div class="lw3d-bar">
  <div class="lw3d-now">
    <div class="lw3d-eyebrow" data-r="eyebrow"></div>
    <h1 data-r="title">Loading</h1>
    <div class="lw3d-stats" data-r="stats"></div>
  </div>
  <div class="lw3d-ctrls">
    <button type="button" data-r="prev" title="Previous program (←)">‹ Prev</button>
    <button type="button" data-r="next" title="Next program (→ or Space)">Next ›</button>
    <button type="button" data-r="look" title="Cycle look (L)"><span>Look</span><b data-r="vlook">Auto</b></button>
    <button type="button" data-r="cam" title="Cycle camera (C)"><span>Camera</span><b data-r="vcam">Auto</b></button>
    <button type="button" data-r="hold" title="Stay on this program (P)" aria-pressed="false">Hold</button>
    <button type="button" data-r="code" title="Show the Logo source (E)">Code</button>
    <button type="button" data-r="full" title="Fullscreen (F)">Fullscreen</button>
  </div>
</div>`;
const PANEL_HTML=`
<div class="lw3d-panel-h"><h2>Flip side</h2><button type="button" data-r="close" title="Close (Esc)">Close</button></div>
<textarea data-r="src" spellcheck="false" aria-label="Logo source"></textarea>
<div class="lw3d-row"><button type="button" class="lw3d-go" data-r="run">Run <kbd>Ctrl ↵</kbd></button><span class="lw3d-msg" data-r="msg">Edit and run. The playlist holds until you press Next.</span></div>
<div class="lw3d-ref">
  <code>FD BK · RT LT</code><span>Move; yaw around the turtle's up axis.</span>
  <code>UP DOWN</code><span>Pitch the nose up or down (degrees).</span>
  <code>RR RL</code><span>Roll right or left around the heading.</span>
  <code>SETXYZ x y z · SETZ</code><span>Move to a point, drawing if the pen is down.</span>
  <code>FACE [x y z]</code><span>Point the heading at a point.</span>
  <code>ZCOR · POS</code><span>POS outputs [x y z].</span>
  <code>HUE deg</code><span>Outputs an [r g b] color. Black pens are colored automatically.</span>
  <code>WAIT n</code><span>Sleep n frames.</span>
  <code>Keys</code><span data-r="keyref"></span>
</div>`;

// ======================= Public API =======================
export const PROGRAMS=Object.freeze(PROGRAM_DEFS.map(p=>Object.freeze({id:p.id,name:p.name})));
export const LOOKS=Object.freeze(LOOK_DEFS.map(l=>Object.freeze({id:l.id,name:l.name})));
export const CAMS=Object.freeze([{id:'auto',name:'Auto'},...CAM_IDS.map(id=>({id,name:id[0].toUpperCase()+id.slice(1)}))].map(Object.freeze));
// Not part of the embedding contract: the compiler and a headless runtime, for tests/run-programs.mjs.
export const internals=Object.freeze({PROGRAM_DEFS,compileProgram,createRuntime});

const DEFAULTS={programs:'all',order:'shuffle',look:'neon',cam:'auto',speed:1,hud:false,keys:false,editor:false,
  maxSegments:400_000,fps:0,onChange:null,onFullscreen:null};
const MAXRUN=95,HOLD=13,OUT=2.6;
// Per-program look preferences used when look is 'random' (indices into LOOK_DEFS).
const LOOK_PREF={'spring-tree':[2,3,0],'fireworks':[0,3],'hilbert-cube':[2,0,1],'nautilus':[2,1,3],'torus-knots':[2,0],'armillary':[1,0,2],
  'menger-sponge':[2,0,1],'sierpinski-tetrahedron':[2,1,0],'h-tree':[0,3,1],'fern-bush':[3,0],'koch-in-3d':[0,2,1]};
const fmtN=n=>n>=1e6?(n/1e6).toFixed(2)+'M':n>=1e4?(n/1e3).toFixed(1)+'k':n.toLocaleString();
function shuffled(a){a=a.slice();for(let i=a.length-1;i>0;i--){const j=Math.floor(Math.random()*(i+1));[a[i],a[j]]=[a[j],a[i]]}return a}

/**
 * Mount the screensaver into `container` (an element). Returns {dispose, pause, resume, next, set}.
 * Throws an Error with .code 'no-webgl2' when WebGL 2 is unavailable (nothing is added to the DOM).
 * Without EXT_color_buffer_float it falls back to 8-bit render targets rather than failing.
 */
export function mount(container,opts={}){
  if(!container||container.nodeType!==1)throw new TypeError('mount(container, opts): container must be an element');
  const o={...DEFAULTS};
  for(const k in opts)if(opts[k]!==undefined)o[k]=opts[k];
  const doc=container.ownerDocument,win=doc.defaultView;

  // ---- GL first, so a failure leaves the DOM untouched.
  const canvas=doc.createElement('canvas');
  canvas.className='lw3d-cv';canvas.style.cssText='width:100%;height:100%;display:block';
  canvas.setAttribute('aria-label','3D turtle graphics screensaver');
  let RT=null,GL;
  try{GL=createRenderer(canvas,o.maxSegments,()=>RT.R.now)}
  catch(e){
    try{canvas.getContext('webgl2')?.getExtension('WEBGL_lose_context')?.loseContext()}catch{}
    if(e.code)throw e;
    const err=new Error('WebGL setup failed: '+e.message,{cause:e});err.code='gl-init';throw err;
  }
  RT=createRuntime(GL);
  const {R}=RT,C=createCamera(GL),cam=C.cam;

  // ---- DOM, all inside one root element.
  const root=doc.createElement('div');root.className='lw3d';
  const style=doc.createElement('style');style.textContent=CSS;
  const hudEl=doc.createElement('div');hudEl.className='lw3d-hud';hudEl.innerHTML=HUD_HTML;
  const panel=doc.createElement('aside');panel.className='lw3d-panel';panel.hidden=true;panel.innerHTML=PANEL_HTML;
  root.append(style,canvas,hudEl,panel);
  const $={};for(const el of root.querySelectorAll('[data-r]'))$[el.dataset.r]=el;
  container.append(root);

  const ac=new AbortController(),sig={signal:ac.signal};
  let keysAC=null,disposed=false,userPaused=false,rafId=0,idleT=0;
  const reducedMQ=win.matchMedia?win.matchMedia('(prefers-reduced-motion: reduce)'):null;

  // ---- option resolution
  const lookIndex=v=>{if(v==='random'||v==='auto')return -1;const i=LOOK_DEFS.findIndex(l=>l.id===v);return i<0?0:i};
  const camIndex=v=>v==='auto'?-1:CAM_IDS.indexOf(v);
  function resolvePool(){
    const ids=o.programs==='all'||o.programs==null?null:Array.isArray(o.programs)?o.programs:[o.programs];
    const pool=ids?ids.map(id=>PROGRAM_DEFS.findIndex(p=>p.id===id)).filter(i=>i>=0):[];
    return pool.length?pool:PROGRAM_DEFS.map((_,i)=>i);
  }
  let pool=resolvePool(),lookLock=lookIndex(o.look),camLock=camIndex(o.cam),hold=false;
  const newOrder=()=>o.order==='sequence'?pool.slice():shuffled(pool);
  const S={idx:-1,order:newOrder(),pos:-1,phase:'run',phaseT:0,prog:null,look:LOOK_DEFS[0],lookIdx:0,acc:0,error:'',custom:false,hueShift:0,runSec:0,progStart:0};
  let clock=0;

  function pickLook(p){
    if(lookLock>=0)return lookLock;
    const pref=LOOK_PREF[p.id]||[0,1,2,3];return pref[Math.floor(Math.random()*pref.length)];
  }
  function start(prog,custom){
    S.prog=prog;S.custom=!!custom;S.phase='run';S.phaseT=0;S.acc=0;S.error='';S.runSec=0;S.hueShift=Math.random();
    S.lookIdx=pickLook(prog);S.look=LOOK_DEFS[S.lookIdx];
    cam.mode=camLock>=0?CAM_IDS[camLock]:prog.cam;cam.zoom=1;cam.chaseOff=null;
    RT.resetWorld();R.frameNo=0;R.now=0;S.progStart=clock;
    try{RT.loadProgram(prog.src);RT.startTask('STARTUP')}
    catch(e){S.error=e.message+(e.line?` (line ${e.line})`:'');R.tasks=[];showMsg(S.error,true);S.phase='hold'}
    updateHUD();syncSrc();
  }
  function go(delta){
    if(S.pos<0)S.pos=0;
    else{
      S.pos+=delta;
      if(S.pos>=S.order.length){
        if(o.order!=='sequence'){const last=S.order[S.order.length-1];S.order=newOrder();if(S.order.length>1&&S.order[0]===last)S.order.push(S.order.shift())}
        S.pos=0;
      }
      if(S.pos<0)S.pos=S.order.length-1;
    }
    S.idx=S.order[S.pos];start(PROGRAM_DEFS[S.idx]);
  }
  function runScheduler(scale){
    if(!R.tasks.length)return;
    S.acc+=S.prog.speed*(+o.speed||1)*scale;const b=Math.floor(S.acc);if(b<1)return;S.acc-=b;
    R.k=0;
    try{
      for(let i=0;i<R.tasks.length;i++)RT.step(R.tasks[i],b);
      R.tasks=R.tasks.filter(t=>!t.done);
      if(R.turtles.some(T=>!T.alive)){R.turtles=R.turtles.filter(T=>T.alive);R.tasks=R.tasks.filter(t=>t.T.alive)}
    }catch(e){S.error=e.message;R.tasks=[];showMsg('Runtime error: '+e.message,true)}
    R.frameNo++;
  }
  const chaseTurtle=()=>!R.tasks.length||R.tasks.length>2?null:R.tasks[0].T;

  // ---- sizing: ResizeObserver on the container x devicePixelRatio (capped at 2)
  let dpr=0;
  function resize(){
    dpr=Math.min(win.devicePixelRatio||1,2);
    const r=root.getBoundingClientRect();
    GL.resize(Math.max(1,Math.round(r.width*dpr)),Math.max(1,Math.round(r.height*dpr)));
  }
  const ro=new win.ResizeObserver(()=>{if(!disposed)resize()});
  ro.observe(container);

  // ---- frame loop
  let last=0,statT=0;
  function frame(now){
    rafId=0;
    if(o.fps>0&&last&&now-last<1000/o.fps-2){rafId=win.requestAnimationFrame(frame);return}
    const raw=last?Math.max(0,(now-last)/1000):0;last=now;
    const dt=Math.min(.1,raw),real=Math.min(.5,raw);
    clock+=real;R.now=clock-S.progStart;
    if(Math.min(win.devicePixelRatio||1,2)!==dpr)resize();
    const reduced=!!reducedMQ?.matches;
    S.phaseT+=dt;
    if(S.phase==='run'){
      // Programs step per frame, as at 60 Hz; with an fps cap, scale up so they draw at the same pace.
      runScheduler(o.fps>0?Math.min(4,real*60):1);S.runSec+=dt;
      if(!R.tasks.length||GL.full||S.runSec>MAXRUN){R.tasks=[];S.phase='hold';S.phaseT=0;updateHUD()}
    }else if(S.phase==='hold'){
      if(!hold&&!S.custom&&S.phaseT>HOLD){S.phase='out';S.phaseT=0}
    }else if(S.phase==='out'){
      if(S.phaseT>OUT)go(1);
    }
    const mode=C.update(dt,clock,S.phase==='run'?chaseTurtle():null,reduced);
    const dist=V.len(V.sub(cam.sEye,cam.sTarget));
    const wave=S.phase==='run'?0:Math.min(1,S.phase==='hold'?S.phaseT/3:1);
    const dissolve=S.phase==='out'?Math.min(1,S.phaseT/(OUT*.85)):0;
    const fade=Math.min(1,clock/1.5)*(S.phase==='out'?Math.max(0,1-Math.max(0,S.phaseT-OUT*.6)/(OUT*.4)):1);
    GL.render({eye:cam.sEye,target:cam.sTarget,up:cam.sUp,fov:FOV,near:Math.max(.5,dist*.01),far:dist*12+cam.radius*6,
      focus:dist,look:S.look,time:R.now,dissolve,wave,fade,hueShift:S.hueShift,camDist:dist,reduced});
    if(o.hud&&now-statT>250){statT=now;updateStats(mode)}
    if(running()&&!rafId)rafId=win.requestAnimationFrame(frame);
  }
  const running=()=>!disposed&&!userPaused&&!doc.hidden;
  function syncRun(){
    if(running()){if(!rafId){last=0;rafId=win.requestAnimationFrame(frame)}}
    else if(rafId){win.cancelAnimationFrame(rafId);rafId=0}
  }
  doc.addEventListener('visibilitychange',syncRun,sig);

  // ---- HUD
  function updateHUD(){
    if(!S.prog)return;
    $.title.textContent=S.prog.name;
    $.eyebrow.textContent=S.custom?'Your program':`Program ${S.pos+1} of ${S.order.length}`;
    $.vlook.textContent=lookLock>=0?LOOK_DEFS[lookLock].name:`Auto · ${S.look.name}`;
    $.vcam.textContent=camLock>=0?CAM_IDS[camLock]:`Auto · ${cam.mode}`;
    $.hold.setAttribute('aria-pressed',String(hold));
  }
  function updateStats(mode){
    const st=S.phase==='run'?'<span class="lw3d-live">drawing</span>':S.error?'<span class="lw3d-live">stopped</span>':'<span>finished</span>';
    $.stats.innerHTML=`${st}<span>turtles <b>${R.tasks.length?R.turtles.length:0}</b></span><span>segments <b>${fmtN(GL.count)}</b></span><span>camera <b>${mode}</b></span>`;
  }
  function applyChrome(){
    hudEl.hidden=!o.hud;
    $.code.hidden=!o.editor;
    if(!o.editor)panel.hidden=true;
    const fs=typeof o.onFullscreen==='function';
    $.full.hidden=!fs;
    $.keyref.textContent='Space/→ next · ← prev · L look · C camera · P hold · E code · '+(fs?'F fullscreen · ':'')+'H hide HUD · drag to orbit · wheel to zoom';
  }

  // idle-hide: the HUD and cursor fade after 3.5 s without input over the mount
  function wake(){
    root.classList.remove('lw3d-idle');clearTimeout(idleT);
    idleT=setTimeout(()=>{if(!disposed&&panel.hidden&&!panel.contains(doc.activeElement))root.classList.add('lw3d-idle')},3500);
  }
  for(const e of ['pointermove','pointerdown','wheel'])root.addEventListener(e,wake,{passive:true,signal:ac.signal});

  // orbit by drag, zoom by wheel
  let drag=null;
  canvas.addEventListener('pointerdown',e=>{drag={x:e.clientX,y:e.clientY};try{canvas.setPointerCapture(e.pointerId)}catch{}},sig);
  canvas.addEventListener('pointermove',e=>{
    if(!drag)return;const dx=e.clientX-drag.x,dy=e.clientY-drag.y;drag={x:e.clientX,y:e.clientY};
    if(cam.lastMode==='chase'){cam.mode='orbit';C.enterOrbit();cam.lastMode='orbit';updateHUD()}
    cam.yaw-=dx*.006;cam.pitch=Math.max(-1.35,Math.min(1.35,cam.pitch+dy*.006));cam.manualUntil=performance.now()+7000;
  },sig);
  canvas.addEventListener('pointerup',()=>drag=null,sig);canvas.addEventListener('pointercancel',()=>drag=null,sig);
  canvas.addEventListener('wheel',e=>{e.preventDefault();cam.zoom=Math.max(.15,Math.min(4,cam.zoom*Math.exp(e.deltaY*.0012)));cam.manualUntil=performance.now()+7000},{passive:false,signal:ac.signal});

  const lookId=()=>lookLock>=0?LOOK_DEFS[lookLock].id:'random';
  const camId=()=>camLock>=0?CAM_IDS[camLock]:'auto';
  function notify(){o.look=lookId();o.cam=camId();if(typeof o.onChange==='function')try{o.onChange({look:o.look,cam:o.cam})}catch(e){console.error(e)}}
  function applyLook(){if(lookLock>=0){S.lookIdx=lookLock;S.look=LOOK_DEFS[lookLock]}else if(S.prog){S.lookIdx=pickLook(S.prog);S.look=LOOK_DEFS[S.lookIdx]}}
  function applyCam(){if(S.prog)cam.mode=camLock>=0?CAM_IDS[camLock]:S.prog.cam}
  function cycleLook(){lookLock=lookLock>=LOOK_DEFS.length-1?-1:lookLock+1;if(lookLock>=0)applyLook();updateHUD();notify()}
  function cycleCam(){camLock=camLock>=CAM_IDS.length-1?-1:camLock+1;applyCam();updateHUD();notify()}
  function toggleHold(){hold=!hold;if(!hold&&S.phase==='hold')S.phaseT=Math.min(S.phaseT,HOLD-3);updateHUD()}
  function fullscreen(){if(typeof o.onFullscreen==='function')o.onFullscreen()}

  // flip side (source editor)
  const src=$.src;let srcDirty=false;
  function syncSrc(){if(!srcDirty&&S.prog)src.value=S.prog.src}
  function showMsg(s,isErr){$.msg.textContent=s;$.msg.classList.toggle('lw3d-err',!!isErr)}
  function togglePanel(open){
    if(!o.editor)open=false;
    panel.hidden=open===undefined?!panel.hidden:!open;
    if(!panel.hidden){syncSrc();src.focus()}
    else if(panel.contains(doc.activeElement))doc.activeElement.blur(); // so shortcut keys work again
    wake();
  }
  src.addEventListener('input',()=>{srcDirty=true},sig);
  function runCustom(){
    srcDirty=false;
    try{compileProgram(src.value)}catch(e){showMsg(`${e.message}${e.line?` (line ${e.line})`:''}`,true);srcDirty=true;return}
    showMsg('Running. Press Next to return to the playlist.');
    start({id:'custom',name:(src.value.match(/^;\s*(.{1,40})/)||[])[1]?.replace(/[.:].*$/,'')||'Untitled',cam:'orbit',speed:S.prog?.speed||6,src:src.value},true);
  }
  src.addEventListener('keydown',e=>{
    if((e.ctrlKey||e.metaKey)&&e.key==='Enter'){e.preventDefault();runCustom()}
    else if(e.key==='Tab'){e.preventDefault();const s=src.selectionStart;src.setRangeText('  ',s,src.selectionEnd,'end');srcDirty=true}
    else if(e.key==='Escape'){togglePanel(false);canvas.focus?.()}
    e.stopPropagation();
  },sig);
  const nav=d=>{srcDirty=false;go(d)};
  const click=(el,f)=>el.addEventListener('click',f,sig);
  click($.prev,()=>nav(-1));click($.next,()=>nav(1));click($.look,cycleLook);click($.cam,cycleCam);click($.hold,toggleHold);
  click($.code,()=>togglePanel());click($.close,()=>togglePanel(false));click($.run,runCustom);click($.full,fullscreen);

  // keyboard shortcuts (keys:true): listened for on the window, but only acted on when focus is in this
  // mount or nowhere in particular (the body), so a mount never steals keys from other inputs on the page.
  function onKey(e){
    const t=e.target;
    if(!(t===doc.body||t===doc.documentElement||t===doc||t===win||root.contains(t)))return;
    wake();
    if(t.closest&&t.closest('textarea,input,select,[contenteditable]'))return;
    if(e.ctrlKey||e.metaKey||e.altKey)return;
    const k=e.key.toLowerCase();
    if(k===' '||k==='arrowright'){e.preventDefault();nav(1)}
    else if(k==='arrowleft')nav(-1);
    else if(k==='l')cycleLook();else if(k==='c')cycleCam();else if(k==='p')toggleHold();
    else if(k==='e')togglePanel();else if(k==='f')fullscreen();
    else if(k==='h')root.classList.add('lw3d-idle');
    else if(k==='escape')togglePanel(false);
  }
  function bindKeys(){
    if(o.keys&&!keysAC){keysAC=new AbortController();win.addEventListener('keydown',onKey,{signal:keysAC.signal})}
    else if(!o.keys&&keysAC){keysAC.abort();keysAC=null}
  }

  // ---- go
  applyChrome();bindKeys();resize();go(0);wake();syncRun();

  const handle={
    dispose(){
      if(disposed)return;
      disposed=true;
      if(rafId)win.cancelAnimationFrame(rafId);rafId=0;
      ac.abort();keysAC?.abort();keysAC=null;ro.disconnect();clearTimeout(idleT);
      GL.dispose();
      root.remove();
      R.tasks=[];R.turtles=[];R.P={};R.G={};R.procs=new Map();R.cur=null;
    },
    pause(){if(disposed)return;userPaused=true;syncRun()},
    resume(){if(disposed)return;userPaused=false;syncRun()},
    next(){if(disposed)return;nav(1)},
    set(p={}){
      if(disposed)return;
      for(const k in p)if(p[k]!==undefined)o[k]=p[k];
      if('maxSegments' in p)GL.setMaxSegments(o.maxSegments);
      if('look' in p){lookLock=lookIndex(o.look);applyLook()}
      if('cam' in p){camLock=camIndex(o.cam);applyCam()}
      if('keys' in p)bindKeys();
      if('hud' in p||'editor' in p||'onFullscreen' in p)applyChrome();
      if('programs' in p||'order' in p){pool=resolvePool();S.order=newOrder();S.pos=-1;srcDirty=false;go(0)}
      updateHUD();
    },
  };
  return handle;
}
