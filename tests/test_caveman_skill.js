#!/usr/bin/env node

const assert = require('assert');
const fs = require('fs');
const path = require('path');

const SKILL_PATH = path.resolve(__dirname, '..', 'skills', 'caveman', 'SKILL.md');
const TEXT_ENCODING = 'utf8';
const SKILL_TEXT = fs.readFileSync(SKILL_PATH, TEXT_ENCODING);
const EXTERNAL_WRITING_RULE = /issue\/defect\/PR\/MR text[^\n]*normal prose/i;
const NO_PERFORMATIVE_WORDS_RULE = /never add words[^\n]*caveman/i;
const EXTERNAL_WRITING_ERROR = 'issue and defect text must be explicitly exempt from caveman prose';
const PERFORMATIVE_WORDS_ERROR = 'compression must forbid adding words merely to sound like a caveman';
const SUCCESS_MESSAGE = 'caveman external-writing rules test passed';

assert.match(
  SKILL_TEXT,
  EXTERNAL_WRITING_RULE,
  EXTERNAL_WRITING_ERROR,
);
assert.match(
  SKILL_TEXT,
  NO_PERFORMATIVE_WORDS_RULE,
  PERFORMATIVE_WORDS_ERROR,
);

console.log(SUCCESS_MESSAGE);
