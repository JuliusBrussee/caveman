# MongoDB Index Design

## Problem

A MongoDB collection `orders` has 50M documents. Queries filter by `customerId` + `status` and sort by `createdAt`. The query is doing a collection scan. Design the right index and explain your reasoning.

```javascript
db.orders.find({ customerId: "abc", status: "shipped" }).sort({ createdAt: -1 }).limit(20)
```
