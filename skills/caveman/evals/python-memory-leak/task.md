# Python Memory Leak

## Problem

A long-running Python web service is slowly consuming more memory over time. It processes CSV uploads, parses them with pandas, and stores results in PostgreSQL. After a few hours, the container gets OOM-killed.

What are the most likely causes and how do you diagnose and fix them?
