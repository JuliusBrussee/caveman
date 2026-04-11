#!/bin/bash
# Caveman Pipe: Summarize anything in technical caveman style
# Usage: git diff | ./commands/caveman-pipe.sh

if [ -t 0 ]; then
  echo "Usage: <command> | ./commands/caveman-pipe.sh"
  exit 1
fi

cat - | claude --print -p "Responde en estilo cavernícola técnico. Sustancia queda. Paja muere."
