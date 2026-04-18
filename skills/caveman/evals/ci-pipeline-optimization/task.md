# CI Pipeline Optimization

## Problem

A GitHub Actions CI pipeline takes 25 minutes. It runs: npm install (3 min), lint (2 min), unit tests (8 min), build (4 min), integration tests (6 min), and deploy (2 min). All steps run sequentially.

How would you optimize this pipeline to run in under 10 minutes?
