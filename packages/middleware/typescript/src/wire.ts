/** Native JSON string-leaf offsets. All bytes outside selected leaves survive. */
export interface StringLeaf { path: (string | number)[]; start: number; end: number; value: string }
export interface WireJSON { value: unknown; strings: Map<string,StringLeaf> }
export const pathKey = (path: (string | number)[]): string => JSON.stringify(path);

export function parseWire(text: string): WireJSON | null {
  if (text.length > 2 << 20) return null;
  try {
    const value: unknown = JSON.parse(text);
    const strings = new Map<string,StringLeaf>();
    let i = 0, nodes = 0;
    const white = () => { while (i < text.length && /[\t\n\r ]/.test(text[i]!)) i++; };
    const string = () => {
      const start = i++;
      while (i < text.length) {
        const char = text[i++];
        if (char === '\\') { i++; continue; }
        if (char === '"') return { start, end: i, value: JSON.parse(text.slice(start,i)) as string };
      }
      throw new Error('unterminated');
    };
    const walk = (path: (string|number)[], depth: number) => {
      if (++nodes > 65536 || depth > 64) throw new Error('bounded');
      white();
      const char = text[i];
      if (char === '"') { strings.set(pathKey(path), { ...string(), path }); return; }
      if (char === '{') {
        i++; white(); const keys = new Set<string>();
        if (text[i] === '}') { i++; return; }
        for (;;) {
          white(); const key = string().value;
          if (keys.has(key)) throw new Error('duplicate');
          keys.add(key); white(); if (text[i++] !== ':') throw new Error('colon');
          walk([...path,key],depth+1); white();
          if (text[i++] === '}') return;
        }
      }
      if (char === '[') {
        i++; white(); let index = 0;
        if (text[i] === ']') { i++; return; }
        for (;;) { walk([...path,index++],depth+1); white(); if (text[i++] === ']') return; }
      }
      while (i < text.length && !/[\t\n\r ,}\]]/.test(text[i]!)) i++;
    };
    walk([],0); white(); if (i !== text.length) return null;
    return { value, strings };
  } catch { return null; }
}

export function patchWire(text: string, patches: { leaf: StringLeaf; replacement: string }[]): string | null {
  const sorted = [...patches].sort((a,b)=>b.leaf.start-a.leaf.start);
  let end = text.length;
  for (const {leaf,replacement} of sorted) {
    if (leaf.start < 0 || leaf.end > end || leaf.start >= leaf.end || JSON.parse(text.slice(leaf.start,leaf.end)) !== leaf.value) return null;
    text = text.slice(0,leaf.start)+JSON.stringify(replacement)+text.slice(leaf.end);
    end = leaf.start;
  }
  return text;
}
