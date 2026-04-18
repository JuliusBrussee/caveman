# Rust Ownership Error

## Problem

This Rust code won't compile:

```rust
fn main() {
    let data = vec![1, 2, 3];
    let handle = std::thread::spawn(|| {
        println!("{:?}", data);
    });
    println!("{:?}", data);
    handle.join().unwrap();
}
```

Explain the error and provide two different fixes.
