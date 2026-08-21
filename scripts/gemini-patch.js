const fs = require('fs');
const path = require('path');

const extensionPath = process.argv[2];
if (!extensionPath) {
  process.exit(0);
}

const sentinel = path.join(extensionPath, '.gemini-patched');
if (fs.existsSync(sentinel)) {
  process.exit(0);
}

const agentsDir = path.join(extensionPath, 'agents');
if (!fs.existsSync(agentsDir)) {
  process.exit(0);
}

const files = fs.readdirSync(agentsDir).filter(f => f.endsWith('.md'));

const mapping = {
  'Read': 'read_file',
  'Edit': 'replace',
  'Write': 'write_file',
  'Grep': 'grep_search',
  'Glob': 'glob',
  'Bash': 'run_shell_command'
};

try {
  for (const f of files) {
    const p = path.join(agentsDir, f);
    let content = fs.readFileSync(p, 'utf8');
    
    // Replace tool names in the tools list
    for (const [oldTool, newTool] of Object.entries(mapping)) {
      const regex = new RegExp(`\\b${oldTool}\\b`, 'g');
      content = content.replace(regex, newTool);
    }
    
    // Replace model names
    content = content.replace(/model: haiku/g, 'model: gemini-1.5-flash');
    
    fs.writeFileSync(p, content);
  }
  
  fs.writeFileSync(sentinel, 'patched');
  console.log('Caveman agents patched for Gemini CLI compatibility.');
} catch (e) {
  // Silent fail during session start to not annoy user
}
