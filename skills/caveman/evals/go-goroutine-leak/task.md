# Go Goroutine Leak

## Problem

A Go HTTP server's goroutine count keeps growing. The handler spawns a goroutine per request to call an external API with a 10-second timeout, but some goroutines never finish.

```go
func handler(w http.ResponseWriter, r *http.Request) {
    ch := make(chan string)
    go func() {
        resp := callExternalAPI()
        ch <- resp
    }()
    select {
    case result := <-ch:
        w.Write([]byte(result))
    case <-time.After(2 * time.Second):
        w.Write([]byte("timeout"))
    }
}
```

Why are goroutines leaking and how do you fix it?
