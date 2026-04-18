# Callback to Async/Await Refactor

## Task

Refactor this callback-based Node.js function to use async/await:

```javascript
function getUser(id, callback) {
  db.query('SELECT * FROM users WHERE id = ?', [id], function(err, rows) {
    if (err) return callback(err);
    if (!rows.length) return callback(new Error('Not found'));
    callback(null, rows[0]);
  });
}
```
