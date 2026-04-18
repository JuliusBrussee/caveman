# DNS Resolution Debugging

## Problem

An application can reach `https://93.184.216.34` by IP but `https://example.com` fails with "could not resolve host." Other applications on the same machine resolve DNS fine. The app runs in a Docker container.

What's the most likely cause and how do you debug it?
