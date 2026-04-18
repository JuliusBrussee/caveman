# Zero-Downtime Database Migration

## Problem

You need to rename a column from `userName` to `user_name` in a PostgreSQL table with 100M rows. The application serves live traffic and you can't take it down. How do you perform this migration with zero downtime?
