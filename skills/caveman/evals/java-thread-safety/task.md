# Java Thread Safety

## Problem

This Java singleton is used in a multi-threaded web server, and users are occasionally seeing each other's data:

```java
public class UserContext {
    private static UserContext instance;
    private String currentUserId;

    public static UserContext getInstance() {
        if (instance == null) instance = new UserContext();
        return instance;
    }

    public void setUser(String userId) { this.currentUserId = userId; }
    public String getUser() { return this.currentUserId; }
}
```

Identify the bugs and provide the fix.
