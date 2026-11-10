// Keep the fork's delivery copies derived from canonical skills and agents.
import { readFileSync, writeFileSync, cpSync, mkdirSync, readdirSync, existsSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { resolve, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { execFileSync } from 'node:child_process';
const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
process.chdir(root);
for (const entry of readdirSync('skills', { withFileTypes: true })) {
  if (!entry.isDirectory() || !existsSync(`skills/${entry.name}/SKILL.md`)) continue;
  if (!['caveman', 'ultracave', 'megacave', 'cavecrew'].includes(entry.name) && !entry.name.startsWith('sweet') && !entry.name.startsWith('caveman-commit') && !entry.name.startsWith('caveman-review') && !entry.name.startsWith('caveman-help') && !entry.name.startsWith('caveman-stats') && entry.name !== 'caveman-compress') continue;
  const dest = `plugins/caveman/skills/${entry.name}`;
  mkdirSync(dest, { recursive: true });
  cpSync(`skills/${entry.name}/SKILL.md`, `${dest}/SKILL.md`);
}
for (const name of ['cavecrew-builder.md', 'cavecrew-investigator.md', 'cavecrew-reviewer.md']) {
  cpSync(`agents/${name}`, `plugins/caveman/agents/${name}`);
}
for (const skill of ['caveman-compress', 'sweet-compress']) {
  const dest = `plugins/caveman/skills/${skill}/scripts`;
  mkdirSync(dest, { recursive: true });
  for (const name of readdirSync('skills/caveman-compress/scripts').filter(name => name.endsWith('.py'))) {
    const source = `skills/caveman-compress/scripts/${name}`;
    if (skill === 'sweet-compress') cpSync(source, `skills/sweet-compress/scripts/${name}`);
    cpSync(source, join(dest, name));
  }
}
const manifest = 'src/hooks/checksums.sha256';
const names = readFileSync(manifest, 'utf8').trim().split('\n').map(line => line.trim().split(/\s+/)[1]);
writeFileSync(manifest, names.map(name => `${createHash('sha256').update(readFileSync(join('src/hooks', name))).digest('hex')}  ${name}\n`).join(''));
// Rebuild archives from scratch: zip updates leave deleted entries behind.
mkdirSync('dist', { recursive: true });
execFileSync('python3', ['-c', `import pathlib,zipfile
for name in ('caveman','sweet'):
 with zipfile.ZipFile('dist/'+name+'.skill','w',zipfile.ZIP_DEFLATED) as archive:
  for path in sorted(pathlib.Path('skills',name).rglob('*')):
   if path.is_file() and '__pycache__' not in path.parts:
    info=zipfile.ZipInfo(str(path.relative_to('skills')),date_time=(2026,1,1,0,0,0))
    info.compress_type=zipfile.ZIP_DEFLATED
    archive.writestr(info,path.read_bytes())
`]);
console.log('Synced Sweet skills, upstream delivery mirrors, hook checksums and skill archives.');
