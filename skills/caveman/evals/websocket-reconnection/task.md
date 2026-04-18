# WebSocket Reconnection Strategy

## Task

A real-time dashboard uses WebSockets to stream live data. Users report the dashboard going blank when their network briefly drops. Design a client-side reconnection strategy with exponential backoff that handles: connection drops, server restarts, and catching up on missed messages.
