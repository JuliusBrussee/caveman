# GraphQL N+1 Query Problem

## Problem

A GraphQL API for a blog returns posts with their authors. For a query requesting 20 posts with author names, the server makes 21 database queries (1 for posts + 1 per author). How do you fix this N+1 problem in GraphQL?
