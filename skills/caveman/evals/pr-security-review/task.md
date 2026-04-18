# Security Code Review

## Task

Review this Express route handler for security issues:

```javascript
app.get('/api/users/:id', (req, res) => {
  const query = `SELECT * FROM users WHERE id = ${req.params.id}`;
  db.query(query).then(user => res.json(user));
});
```
