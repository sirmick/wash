// The Questions tab's input is one Markdown document, every thread under a
// `## node · id — title` heading (swarm.WriteQAThread). Cut it into its
// threads so each is drawn as its own card; pure, tested without a DOM.

export interface QASection {
  /** The thread's id, from its heading; '' when the heading is not one WriteQAThread wrote. */
  id: string;
  node: string;
  title: string;
  /** The thread's Markdown below its heading. */
  body: string;
}

export interface QASplit {
  /** Whatever comes before the first thread: the document's title and note. */
  preamble: string;
  threads: QASection[];
}

const heading = /^(.*?) · (\S+) — (.*)$/;

export function splitQAMarkdown(markdown: string): QASplit {
  const lines = markdown.split('\n');
  const out: QASplit = { preamble: '', threads: [] };
  let cur: QASection | null = null;
  const pre: string[] = [];
  const body: string[] = [];
  const flush = () => {
    if (cur) { cur.body = body.join('\n').trim(); out.threads.push(cur); }
    body.length = 0;
  };
  for (const line of lines) {
    if (line.startsWith('## ')) {
      flush();
      const m = heading.exec(line.slice(3).trim());
      cur = m ? { id: m[2], node: m[1], title: m[3], body: '' } : { id: '', node: '', title: line.slice(3).trim(), body: '' };
      continue;
    }
    (cur ? body : pre).push(line);
  }
  flush();
  out.preamble = pre.join('\n').trim();
  return out;
}
