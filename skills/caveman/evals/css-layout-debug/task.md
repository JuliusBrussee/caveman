# CSS Flexbox Layout Bug

## Problem

A navigation bar should have the logo on the left, menu items centered, and a login button on the right. But the centered items aren't truly centered — they're shifted right because the logo takes up space.

```css
.navbar {
  display: flex;
  align-items: center;
}
.logo { margin-right: auto; }
.menu { display: flex; gap: 1rem; }
.login { margin-left: auto; }
```

How do you achieve true center alignment for the menu items regardless of logo/button widths?
