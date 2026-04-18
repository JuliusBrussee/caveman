# Terraform State Locking

## Problem

Two team members ran `terraform apply` at the same time and corrupted the state file. How do you set up state locking with a remote backend to prevent this, and what do you do if a lock gets stuck?
